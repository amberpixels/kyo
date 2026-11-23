package render

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/amberpixels/kyo/internal/aggregator"
)

// metaWidth is the column right-aligned meta (the clock) aligns to, so it sits
// near the content instead of at the far edge of a wide terminal. Narrower
// terminals use their own width.
const metaWidth = 74

// playNameMax caps the play-name column; longer names are cut.
const playNameMax = 28

// taskTimerMin is how old the current task must be before it shows a timer,
// because quick tasks would only flicker 0:00.
const taskTimerMin = 10 * time.Second

// playBarWidth is the cell width of the running play's progress bar.
const playBarWidth = 10

// barMinWidth is the narrowest terminal that still draws the progress bar;
// below it the bar would push the task label out of view.
const barMinWidth = 60

// The progress bar's glyphs, ele's: they differ without color too.
const (
	barFill  = "█"
	barTrack = "░"
)

// Opts controls a rendered frame.
type Opts struct {
	Width int // terminal width; lines are cut to it (0 = no limit)
	// MaxLines caps the block's height (0 = no limit). The block is repainted
	// by moving the cursor back up over it, which only works while it is
	// shorter than the screen, so callers pass the height minus one.
	MaxLines  int
	Spinner   rune          // the running play's glyph; 0 draws a plain marker
	Elapsed   time.Duration // wall time since the run started; 0 hides the clock
	ArgsCheck bool          // --check in the args, for before the playbook event
	Styles    *Styles       // nil = plain
}

// Frame renders the live block: a header, a row per play with the running
// play's current task and hosts under it, the counters, and the warnings count.
func Frame(s aggregator.Snapshot, opt Opts) []string {
	st := opt.Styles
	if st == nil {
		st = plainStyles()
	}

	head := []string{headerLine(st, s, opt), ""}
	plays := playRows(st, s, opt)
	// The rule takes the blank line's place, so the block keeps its height.
	tail := []string{ruleLine(st, opt.Width), "  " + countersLine(st, s.Totals)}
	if s.WarnTotal > 0 {
		tail = append(tail, "  "+warningsLine(st, s))
	}

	// Finished plays are the block's elastic part: on a short terminal they
	// fold into one row, so the running play and the counters stay in view.
	if opt.MaxLines > 0 && len(head)+len(plays)+len(tail) > opt.MaxLines {
		plays = foldDone(st, s, opt)
	}

	lines := slices.Concat(head, plays, tail)
	for i, l := range lines {
		lines[i] = truncateLine(l, opt.Width)
	}
	// Last resort for a terminal too short even for that: keep the top.
	if opt.MaxLines > 0 && len(lines) > opt.MaxLines {
		lines = lines[:opt.MaxLines]
	}
	return lines
}

// headerLine is "file · plan (--check) · N hosts", the clock right-aligned.
func headerLine(st *Styles, s aggregator.Snapshot, opt Opts) string {
	file := s.File
	if file == "" {
		file = "ansible-playbook"
	}
	parts := []string{st.title.Render(file), modeLabel(s, opt.ArgsCheck)}
	if n := hostCount(s); n > 0 {
		parts = append(parts, plural(n, "host"))
	}
	return rightAlign("  "+strings.Join(parts, " · "), clock(st, opt.Elapsed), opt.Width)
}

// IsPlan is the plugin's check flag, else --check in the args before the
// playbook event has said.
func IsPlan(s aggregator.Snapshot, argsCheck bool) bool {
	if s.File != "" || !s.Started.IsZero() {
		return s.Check
	}
	return argsCheck
}

func modeLabel(s aggregator.Snapshot, argsCheck bool) string {
	if IsPlan(s, argsCheck) {
		return "plan (--check)"
	}
	return "apply"
}

// hostCount is every host the run has named: in a play's host list or in a
// result.
func hostCount(s aggregator.Snapshot) int {
	seen := map[string]bool{}
	for _, p := range s.Plays {
		for _, h := range p.Hosts {
			seen[h] = true
		}
	}
	for _, h := range s.Hosts {
		seen[h.Host] = true
	}
	return len(seen)
}

// playRows is a row per play, plus the running play's hosts line.
func playRows(st *Styles, s aggregator.Snapshot, opt Opts) []string {
	nameW := 0
	for _, p := range s.Plays {
		nameW = max(nameW, min(ansi.StringWidth(playName(p)), playNameMax))
	}
	// With a bar, counts sit left-aligned after it, and finished rows leave the
	// bar's column blank so every count starts in the same column.
	withBar := (opt.Width <= 0 || opt.Width >= barMinWidth) &&
		slices.ContainsFunc(s.Plays, func(p aggregator.PlayProgress) bool { return !p.Ended })
	var rows []string
	for _, p := range s.Plays {
		name := padRight(ansi.Truncate(playName(p), playNameMax, "…"), nameW)
		count := fmt.Sprintf("%7s", progress(p))
		if withBar {
			count = progress(p)
		}
		if p.Ended {
			if withBar {
				name += strings.Repeat(" ", playBarWidth+1)
			}
			rows = append(rows, fmt.Sprintf("  %s %s %s  %s",
				st.done.Render("✓"), name, count, st.took.Render(clockText(p.Took))))
			continue
		}
		row := fmt.Sprintf("  %s %s", st.spin.Render(spin(opt.Spinner)), name)
		if withBar {
			row += " " + playBar(st, p) + " " + count
			if pc := percent(p); pc != "" {
				row += "  " + st.pct.Render(pc)
			}
		} else {
			row += " " + count
		}
		if s.Current == nil {
			rows = append(rows, row)
			continue
		}
		indent := ansi.StringWidth(row) + 2
		row += "  " + currentLabel(st, s.Current)
		if s.Current.Elapsed >= taskTimerMin {
			row += "  " + clock(st, s.Current.Elapsed)
		}
		rows = append(rows, row)
		if hl := hostsLine(st, s.Current, opt.Width-indent); hl != "" {
			rows = append(rows, strings.Repeat(" ", indent)+hl)
		}
	}
	return rows
}

// foldDone is playRows with every finished play folded into one row.
func foldDone(st *Styles, s aggregator.Snapshot, opt Opts) []string {
	var running aggregator.Snapshot
	running.Current = s.Current
	done := 0
	var took time.Duration
	for _, p := range s.Plays {
		if p.Ended {
			done++
			took += p.Took
			continue
		}
		running.Plays = append(running.Plays, p)
	}
	rows := playRows(st, running, opt)
	if done > 0 {
		row := fmt.Sprintf(
			"  %s %s  %s",
			st.done.Render("✓"),
			plural(done, "play")+" done",
			st.took.Render(clockText(took)),
		)
		rows = append([]string{row}, rows...)
	}
	return rows
}

func playName(p aggregator.PlayProgress) string {
	if p.Name == "" {
		return "(unnamed play)"
	}
	return p.Name
}

// progress is "done/total", with a "+" while the total can still grow
// (an include_role whose size was unknown), so it does not pass as final.
func progress(p aggregator.PlayProgress) string {
	t := strconv.Itoa(p.Total)
	if !p.Exact && !p.Ended {
		t += "+"
	}
	return strconv.Itoa(p.Done) + "/" + t
}

// playBar is the running play's progress as filled cells over a track. An
// inexact total can still grow, so its bar never reads full.
func playBar(st *Styles, p aggregator.PlayProgress) string {
	filled := 0
	if p.Total > 0 {
		filled = min(p.Done*playBarWidth/p.Total, playBarWidth)
		if !p.Exact && filled == playBarWidth {
			filled--
		}
	}
	return st.bar.Render(strings.Repeat(barFill, filled)) +
		st.track.Render(strings.Repeat(barTrack, playBarWidth-filled))
}

// percent is the running play's "NN%", empty while the total is unknown or
// can still grow, because a share of a moving total would mislead.
func percent(p aggregator.PlayProgress) string {
	if p.Total <= 0 || !p.Exact {
		return ""
	}
	return strconv.Itoa(min(p.Done*100/p.Total, 100)) + "%"
}

func spin(r rune) string {
	if r == 0 {
		return "▸"
	}
	return string(r)
}

// taskLabel is "[role] name", with handlers marked.
func taskLabel(role, name string, handler bool) string {
	if role != "" {
		name = "[" + role + "] " + name
	}
	if handler {
		name += " (handler)"
	}
	return name
}

// currentLabel is the running task's label with its role dimmed, so the
// task's name reads first.
func currentLabel(st *Styles, cur *aggregator.CurrentTask) string {
	label := taskLabel("", cur.Name, cur.Handler)
	if cur.Role != "" {
		label = st.dim.Render("["+cur.Role+"]") + " " + label
	}
	return label
}

// hostsLine lists the current task's hosts on one line: those that reported,
// in arrival order with their outcome, then those still due. What doesn't fit
// in width folds into a "+N" count.
func hostsLine(st *Styles, cur *aggregator.CurrentTask, width int) string {
	var items []string
	for _, r := range cur.Reported {
		items = append(items, r.Host+" "+st.status(r.Status))
	}
	for _, h := range cur.Pending {
		items = append(items, st.dim.Render(h+" …"))
	}
	if len(items) == 0 {
		return ""
	}
	if width <= 0 {
		return strings.Join(items, "  ")
	}
	line := ""
	for i, it := range items {
		next := it
		if line != "" {
			next = line + "  " + it
		}
		more := ""
		if i < len(items)-1 {
			more = st.dim.Render(fmt.Sprintf("  +%d", len(items)-i-1))
		}
		if ansi.StringWidth(next)+ansi.StringWidth(more) > width {
			return line + st.dim.Render(fmt.Sprintf("  +%d", len(items)-i))
		}
		line = next
	}
	return line
}

// ruleLine sets the counters apart from the plays: a faint rule from the
// indent to the meta column, or the terminal's edge when that is nearer.
func ruleLine(st *Styles, width int) string {
	edge := metaWidth
	if width > 0 && width < metaWidth {
		edge = width
	}
	return "  " + st.dim.Render(strings.Repeat("─", max(edge-2, 0)))
}

// countersLine is the run's totals; ignored and rescued show only when set.
func countersLine(st *Styles, c aggregator.Counters) string {
	parts := []string{
		counter(st, st.ok, "ok", c.OK),
		counter(st, st.changed, "changed", c.Changed),
		counter(st, st.skipped, "skipped", c.Skipped),
		counter(st, st.failed, "failed", c.Failed),
		counter(st, st.failed, "unreachable", c.Unreachable),
	}
	if c.Ignored > 0 {
		parts = append(parts, counter(st, st.muted, "ignored", c.Ignored))
	}
	if c.Rescued > 0 {
		parts = append(parts, counter(st, st.muted, "rescued", c.Rescued))
	}
	return strings.Join(parts, "   ")
}

// counter is "label n" in its color when n is nonzero, dim when it is zero.
func counter(st *Styles, style lipgloss.Style, label string, n int) string {
	return pick(n > 0, style, st.dim).Render(label + " " + strconv.Itoa(n))
}

func warningsLine(st *Styles, s aggregator.Snapshot) string {
	line := "warnings " + strconv.Itoa(s.WarnTotal)
	if s.Deprecations > 0 {
		line += fmt.Sprintf(" (%d deprecation)", s.Deprecations)
	}
	return st.warn.Render(line)
}

// clockText is m:ss, or h:mm:ss from an hour on.
func clockText(d time.Duration) string {
	d = d.Truncate(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// clock is the styled clock, empty for a zero duration.
func clock(st *Styles, d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return st.timer.Render(clockText(d))
}

// rightAlign puts right at the meta column, or two spaces after left when
// there is no room.
func rightAlign(left, right string, width int) string {
	if right == "" {
		return left
	}
	edge := metaWidth
	if width > 0 && width < metaWidth {
		edge = width
	}
	gap := max(edge-ansi.StringWidth(left)-ansi.StringWidth(right), 2)
	return left + strings.Repeat(" ", gap) + right
}

func padRight(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func pick[T any](cond bool, yes, no T) T {
	if cond {
		return yes
	}
	return no
}

// truncateLine cuts s to width cells, ANSI-aware; width 0 means no limit.
func truncateLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}
