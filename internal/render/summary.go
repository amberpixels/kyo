package render

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/amberpixels/kyo/internal/aggregator"
)

// Summary limits: warning groups listed, diff lines per change, and the
// task-label column the diff counts align to.
const (
	summaryMaxWarnings = 10
	summaryDiffLines   = 40
	labelColumnMax     = 48
)

// SummaryOpts controls the summary.
type SummaryOpts struct {
	LogPath   string        // raw log; omitted when empty
	Elapsed   time.Duration // run time, right-aligned on the headline; 0 hides it
	Width     int           // terminal width; long lines are cut to it (0 = no limit)
	ArgsCheck bool          // --check in the args, when no playbook event said
	Styles    *Styles       // nil = plain
}

// Summary prints the end-of-run report once, as plain lines that survive
// scrollback: the headline, failures and unreachable hosts, what changed (or
// would change) per host with diff sizes and, under --diff, the diffs, then
// grouped warnings and the raw log path.
func Summary(w io.Writer, s aggregator.Snapshot, opt SummaryOpts) {
	st := opt.Styles
	if st == nil {
		st = plainStyles()
	}
	out := func(format string, a ...any) { line(w, fmt.Sprintf(format, a...), opt.Width) }
	plan := isPlan(s, opt.ArgsCheck)

	out("%s", rightAlign("  "+headline(st, s, plan), clock(st, opt.Elapsed), opt.Width))

	if failed := unrescued(s.Failures); len(failed) > 0 {
		out("")
		out("  %s", st.failed.Render("failed"))
		hostW := hostWidth(failed)
		for _, f := range failed {
			out("    %s  %s%s", padRight(f.Host, hostW), taskLabel(f.Role, f.Task, false), msgSuffix(f.Msg))
		}
	}
	if len(s.Unreachable) > 0 {
		out("")
		out("  %s", st.failed.Render("unreachable"))
		hostW := hostWidth(s.Unreachable)
		for _, f := range s.Unreachable {
			out("    %s  %s%s", padRight(f.Host, hostW), taskLabel(f.Role, f.Task, false), msgSuffix(f.Msg))
		}
	}
	if note := caughtNote(s); note != "" {
		out("")
		out("  %s", st.dim.Render(note))
	}

	changes(w, st, s.Changes, opt.Width)

	if len(s.Warnings) > 0 {
		out("")
		out("  %s", st.warn.Render("warnings"))
		for _, g := range s.Warnings[:min(len(s.Warnings), summaryMaxWarnings)] {
			out("    %s", warningLine(st, g))
		}
		if hidden := len(s.Warnings) - summaryMaxWarnings; hidden > 0 {
			out("    %s", st.dim.Render(fmt.Sprintf("+%d more (raw log)", hidden)))
		}
	}

	if opt.LogPath != "" {
		out("")
		out("  %s", st.dim.Render("raw log: "+opt.LogPath))
	}
}

// line prints s cut to width, without trailing blanks (a diff's empty
// context line would otherwise keep its indent).
func line(w io.Writer, s string, width int) {
	fmt.Fprintln(w, strings.TrimRight(truncateLine(s, width), " "))
}

// headline is "plan: 3 changes on 2 hosts, nothing failed", or the apply
// form with "changed".
func headline(st *Styles, s aggregator.Snapshot, plan bool) string {
	mode := "apply"
	if plan {
		mode = "plan"
	}
	what := "no changes"
	if n := len(s.Changes); n > 0 {
		hosts := map[string]bool{}
		for _, c := range s.Changes {
			hosts[c.Host] = true
		}
		noun := plural(n, "change")
		if !plan {
			noun = fmt.Sprintf("%d changed", n)
		}
		what = noun + " on " + plural(len(hosts), "host")
	}

	var bad []string
	if n := len(unrescued(s.Failures)); n > 0 {
		bad = append(bad, fmt.Sprintf("%d failed", n))
	}
	if n := len(s.Unreachable); n > 0 {
		bad = append(bad, fmt.Sprintf("%d unreachable", n))
	}
	if len(bad) == 0 {
		return st.success.Render(mode + ": " + what + ", nothing failed")
	}
	return st.failed.Render(mode + ": " + what + ", " + strings.Join(bad, ", "))
}

func unrescued(fs []aggregator.Failure) []aggregator.Failure {
	var out []aggregator.Failure
	for _, f := range fs {
		if !f.Rescued {
			out = append(out, f)
		}
	}
	return out
}

// caughtNote counts the failures that did not fail the run: rescued by a
// block, or ignored with ignore_errors.
func caughtNote(s aggregator.Snapshot) string {
	rescued := len(s.Failures) - len(unrescued(s.Failures))
	var parts []string
	if rescued > 0 {
		parts = append(parts, plural(rescued, "failure")+" rescued")
	}
	if n := len(s.Ignored); n > 0 {
		parts = append(parts, plural(n, "failure")+" ignored")
	}
	return strings.Join(parts, ", ")
}

func hostWidth(fs []aggregator.Failure) int {
	w := 0
	for _, f := range fs {
		w = max(w, ansi.StringWidth(f.Host))
	}
	return w
}

// changes prints the per-host list: "~ [role] task  (diff: +4 -1)", and under
// it the diff text when the run had --diff. A later host whose diff for the
// same task is identical points at the first instead of repeating it.
func changes(w io.Writer, st *Styles, cs []aggregator.Change, width int) {
	if len(cs) == 0 {
		return
	}
	byHost := map[string][]aggregator.Change{}
	labelW := 0
	for _, c := range cs {
		byHost[c.Host] = append(byHost[c.Host], c)
		labelW = max(labelW, min(ansi.StringWidth(taskLabel(c.Role, c.Task, c.Handler)), labelColumnMax))
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	slices.Sort(hosts)

	type diffKey struct{ play, role, task, text string }
	firstHost := map[diffKey]string{}
	out := func(s string) { line(w, s, width) }

	for _, h := range hosts {
		out("")
		out("  " + st.title.Render(h))
		for _, c := range byHost[h] {
			row := "    " + st.changed.Render("~") + " " + taskLabel(c.Role, c.Task, c.Handler)
			if d := diffCount(c); d != "" {
				row = "    " + st.changed.Render("~") + " " + padRight(taskLabel(c.Role, c.Task, c.Handler), labelW) +
					"  " + st.dim.Render(d)
			}
			out(row)
			if c.DiffText == "" {
				continue
			}
			k := diffKey{c.Play, c.Role, c.Task, c.DiffText}
			if first, ok := firstHost[k]; ok {
				out("      " + st.dim.Render("(same diff as "+first+")"))
				continue
			}
			firstHost[k] = h
			for _, l := range diffLines(st, c.DiffText) {
				out("      " + l)
			}
		}
	}
}

// diffCount is "(diff: +4 -1)", leaving out a zero side.
func diffCount(c aggregator.Change) string {
	if c.Diff == nil {
		return ""
	}
	var parts []string
	if c.Diff.Added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", c.Diff.Added))
	}
	if c.Diff.Removed > 0 {
		parts = append(parts, fmt.Sprintf("-%d", c.Diff.Removed))
	}
	if len(parts) == 0 {
		return "(diff)"
	}
	return "(diff: " + strings.Join(parts, " ") + ")"
}

// diffLines colors a unified diff and caps it at summaryDiffLines.
func diffLines(st *Styles, text string) []string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	hidden := 0
	if len(lines) > summaryDiffLines {
		hidden = len(lines) - summaryDiffLines
		lines = lines[:summaryDiffLines]
	}
	out := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			out = append(out, st.dim.Render(l))
		case strings.HasPrefix(l, "+"):
			out = append(out, st.added.Render(l))
		case strings.HasPrefix(l, "-"):
			out = append(out, st.removed.Render(l))
		case strings.HasPrefix(l, "@@"):
			out = append(out, st.hunk.Render(l))
		default:
			out = append(out, l)
		}
	}
	if hidden > 0 {
		out = append(out, st.dim.Render(fmt.Sprintf("… %d more lines (raw log)", hidden)))
	}
	return out
}

// warningLine is "3× deprecation: <first sentence>  (origin or hosts)".
func warningLine(st *Styles, g aggregator.WarningGroup) string {
	text := g.Line
	if g.Kind == aggregator.KindDeprecation {
		text = "deprecation: " + text
	}
	row := fmt.Sprintf("%d× %s", g.Count, text)
	switch {
	case g.Origin != "":
		row += "  " + st.dim.Render("("+g.Origin+")")
	case len(g.Hosts) > 0 && len(g.Hosts) <= 3:
		row += "  " + st.dim.Render("("+strings.Join(g.Hosts, ", ")+")")
	case len(g.Hosts) > 3:
		row += "  " + st.dim.Render("("+plural(len(g.Hosts), "host")+")")
	}
	return row
}
