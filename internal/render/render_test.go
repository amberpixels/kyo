package render

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/events"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

// golden compares got with testdata/<name>.golden, or rewrites it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file (run go test -update to accept):\n--- got\n%s\n--- want\n%s",
			name, got, want)
	}
}

// replay feeds the aggregator's recorded fixture up to limit lines (0 = all),
// with the clock one second after the last event fed, so elapsed times are
// stable.
func replay(t *testing.T, name string, limit int) aggregator.Snapshot {
	t.Helper()
	var last time.Time
	agg := aggregator.New(func() time.Time { return last.Add(time.Second) })
	f, err := os.Open(filepath.Join("..", "aggregator", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for n := 0; sc.Scan() && (limit <= 0 || n < limit); n++ {
		ev, err := events.Decode(sc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if ev.Time > 0 {
			last = time.Unix(0, int64(ev.Time*float64(time.Second)))
		}
		agg.Feed(ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if limit <= 0 {
		agg.Finish(last)
	}
	return agg.Snapshot()
}

func frameText(s aggregator.Snapshot, opt Opts) string {
	return strings.Join(Frame(s, opt), "\n") + "\n"
}

func TestFrameGolden(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		limit   int
		opt     Opts
	}{
		// the failure on web-1 is in, web-2 still due
		{"frame-two-hosts-mid-task", "two-hosts.jsonl", 7, Opts{Width: 100, Elapsed: 102 * time.Second}},
		// an include_role made the total inexact
		{"frame-two-hosts-include", "two-hosts.jsonl", 12, Opts{Width: 100, Elapsed: 2 * time.Minute}},
		// first play done, second running on its second serial batch
		{
			"frame-two-hosts-second-play",
			"two-hosts.jsonl",
			23,
			Opts{Width: 100, Spinner: '⠼', Elapsed: 6 * time.Second},
		},
		{"frame-basic-mid", "basic.jsonl", 12, Opts{Width: 100, Elapsed: time.Second}},
		{"frame-flow-done", "flow.jsonl", 0, Opts{Width: 100, Elapsed: 2 * time.Second}},
		// header and counters cut to a narrow terminal
		{"frame-narrow", "two-hosts.jsonl", 7, Opts{Width: 40, Elapsed: 102 * time.Second}},
		// before the playbook event: the mode comes from the args
		{"frame-before-playbook", "two-hosts.jsonl", 0, Opts{Width: 100, ArgsCheck: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s aggregator.Snapshot
			if c.name != "frame-before-playbook" {
				s = replay(t, c.fixture, c.limit)
			}
			golden(t, c.name, frameText(s, c.opt))
		})
	}
}

// manyHosts is a run with six finished plays and a running one on twenty hosts.
func manyHosts() aggregator.Snapshot {
	var hosts []string
	for i := 1; i <= 20; i++ {
		hosts = append(hosts, fmt.Sprintf("app-%02d", i))
	}
	s := aggregator.Snapshot{File: "site.yml", Totals: aggregator.Counters{OK: 410, Changed: 12, Skipped: 80}}
	for i := 1; i <= 6; i++ {
		s.Plays = append(s.Plays, aggregator.PlayProgress{
			Name: fmt.Sprintf("Stage %d", i), Done: 5, Total: 5, Exact: true, Hosts: hosts,
			Ended: true, Took: time.Duration(i) * 10 * time.Second,
		})
	}
	s.Plays = append(
		s.Plays,
		aggregator.PlayProgress{Name: "Deploy apps", Done: 3, Total: 9, Exact: true, Hosts: hosts},
	)
	s.Current = &aggregator.CurrentTask{
		Name: "Read the current env files", Role: "app_env", Elapsed: 14 * time.Second,
		Reported: []aggregator.HostStatus{
			{Host: "app-01", Status: events.StatusOK},
			{Host: "app-02", Status: events.StatusChanged},
			{Host: "app-03", Status: events.StatusFailed},
		},
		Pending: hosts[3:],
	}
	return s
}

func TestFrameFoldsOnShortTerminal(t *testing.T) {
	s := manyHosts()
	tall := Frame(s, Opts{Width: 80, MaxLines: 40, Elapsed: 3 * time.Minute})
	short := Frame(s, Opts{Width: 80, MaxLines: 8, Elapsed: 3 * time.Minute})
	if len(short) > 8 {
		t.Fatalf("frame has %d lines, want at most 8", len(short))
	}
	golden(t, "frame-many-hosts-tall", strings.Join(tall, "\n")+"\n")
	golden(t, "frame-many-hosts-short", strings.Join(short, "\n")+"\n")
	for _, l := range append(tall, short...) {
		if w := len([]rune(l)); w > 80 {
			t.Errorf("line wider than 80: %q", l)
		}
	}
}

func TestFrameTinyTerminalKeepsTop(t *testing.T) {
	lines := Frame(manyHosts(), Opts{Width: 80, MaxLines: 2})
	if len(lines) != 2 || !strings.Contains(lines[0], "site.yml") {
		t.Errorf("got %q", lines)
	}
}

func TestSummaryGolden(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		opt     SummaryOpts
	}{
		// plan with a failure, an unreachable host, an ignored one and warnings
		{
			"summary-two-hosts",
			"two-hosts.jsonl",
			SummaryOpts{LogPath: "kyo-20261123-120000.log", Elapsed: 125 * time.Second, Width: 100},
		},
		// apply with --diff: diff text under each change
		{"summary-basic-diff", "basic.jsonl", SummaryOpts{LogPath: "kyo.log", Elapsed: 3 * time.Second, Width: 100}},
		// rescued failures, loop and role diffs
		{"summary-flow", "flow.jsonl", SummaryOpts{Width: 100}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			Summary(&buf, replay(t, c.fixture, 0), c.opt)
			golden(t, c.name, buf.String())
		})
	}
}

func TestSummaryCleanPlan(t *testing.T) {
	var buf bytes.Buffer
	s := aggregator.Snapshot{File: "site.yml", Check: true, Hosts: []aggregator.HostCounters{{Host: "web-1"}}}
	Summary(&buf, s, SummaryOpts{LogPath: "run.log"})
	want := "  plan: no changes, nothing failed\n\n  raw log: run.log\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestSummaryApplyFallsBackToArgs(t *testing.T) {
	var buf bytes.Buffer
	Summary(&buf, aggregator.Snapshot{}, SummaryOpts{ArgsCheck: true})
	if !strings.HasPrefix(buf.String(), "  plan: no changes") {
		t.Errorf("got %q", buf.String())
	}
}

func TestSummaryDiffCapAndRepeat(t *testing.T) {
	var long []string
	for i := range 50 {
		long = append(long, fmt.Sprintf("+line %d", i))
	}
	text := strings.Join(long, "\n")
	stat := &events.DiffStat{Added: 50}
	s := aggregator.Snapshot{
		File: "site.yml", Check: true,
		Changes: []aggregator.Change{
			{Play: "p", Host: "web-1", Task: "Write the config", Diff: stat, DiffText: text},
			{Play: "p", Host: "web-2", Task: "Write the config", Diff: stat, DiffText: text},
			{Play: "p", Host: "web-3", Task: "Write the config", Diff: stat, DiffText: "+other"},
		},
	}
	var buf bytes.Buffer
	Summary(&buf, s, SummaryOpts{})
	golden(t, "summary-diff-cap", buf.String())
}

func TestSummaryWarningsCapped(t *testing.T) {
	s := aggregator.Snapshot{File: "site.yml"}
	for i := range 12 {
		s.Warnings = append(s.Warnings, aggregator.WarningGroup{
			Kind: aggregator.KindWarning, Line: fmt.Sprintf("warning number %d", i), Count: 12 - i,
			Hosts: []string{"web-1", "web-2", "web-3", "web-4"},
		})
	}
	var buf bytes.Buffer
	Summary(&buf, s, SummaryOpts{})
	out := buf.String()
	if !strings.Contains(out, "+2 more (raw log)") || strings.Contains(out, "warning number 10") ||
		!strings.Contains(out, "12× warning number 0  (4 hosts)") {
		t.Errorf("got:\n%s", out)
	}
}

// TestPlainMilestones replays a run event by event, a second apart, and checks
// plain mode prints milestones only: start, the failure, the unreachable host,
// each finished play, and a progress line after a quiet spell.
func TestPlainMilestones(t *testing.T) {
	var buf bytes.Buffer
	p := NewPlainProgress(&buf, nil, false)
	f, err := os.ReadFile(filepath.Join("..", "aggregator", "testdata", "two-hosts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Count(bytes.TrimSpace(f), []byte("\n")) + 1
	for n := 1; n <= lines; n++ {
		p.Update(replay(t, "two-hosts.jsonl", n), time.Duration(n)*time.Second)
	}
	p.Update(replay(t, "two-hosts.jsonl", 0), time.Duration(lines+20)*time.Second)
	golden(t, "plain-two-hosts", buf.String())
}

func TestPlainEnv(t *testing.T) {
	var buf bytes.Buffer
	if !Plain(&buf, nil) {
		t.Error("a buffer is not a terminal")
	}
	if !Plain(os.Stderr, []string{"KYO_PLAIN=1"}) || !Plain(os.Stderr, []string{"CLAUDECODE=1"}) ||
		!Plain(os.Stderr, []string{"CI=true"}) {
		t.Error("KYO_PLAIN, CLAUDECODE and CI must each force plain mode")
	}
	if truthy("0") || truthy("false") || !truthy("1") {
		t.Error("truthy")
	}
}

func TestNoColor(t *testing.T) {
	if st := NewStyles(os.Stderr, []string{"NO_COLOR=1"}); st.failed.Render("x") != "x" {
		t.Error("NO_COLOR must render plain")
	}
}

// colorStyles are the colored styles forced to true color, as a terminal
// would get them.
func colorStyles() *Styles {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	return newStyles(r)
}

func TestCountersColoredByValue(t *testing.T) {
	st := colorStyles()
	line := countersLine(st, aggregator.Counters{OK: 3, Skipped: 2, Rescued: 1})
	for _, want := range []string{
		st.ok.Render("ok 3"),
		st.skipped.Render("skipped 2"),
		st.muted.Render("rescued 1"),
		st.dim.Render("changed 0"),
		st.dim.Render("failed 0"),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("counters %q lack %q", line, want)
		}
	}
	if st.ok.Render("x") == "x" || st.skipped.Render("x") == "x" {
		t.Error("a nonzero counter must carry color")
	}
}

func TestRunningRowColored(t *testing.T) {
	st := colorStyles()
	s := manyHosts()
	rows := playRows(st, s, Opts{Width: 100, Spinner: '⠼'})
	row := rows[len(rows)-2]
	for _, want := range []string{
		st.spin.Render("⠼"),
		st.bar.Render(strings.Repeat(barFill, 3)),
		st.pct.Render("33%"),
		st.dim.Render("[app_env]"),
	} {
		if !strings.Contains(row, want) {
			t.Errorf("running row %q lacks %q", row, want)
		}
	}
}

func TestPlayBar(t *testing.T) {
	cases := []struct {
		p       aggregator.PlayProgress
		filled  int
		percent string
	}{
		{aggregator.PlayProgress{Done: 0, Total: 9, Exact: true}, 0, "0%"},
		{aggregator.PlayProgress{Done: 3, Total: 9, Exact: true}, 3, "33%"},
		{aggregator.PlayProgress{Done: 9, Total: 9, Exact: true}, playBarWidth, "100%"},
		// a total that can still grow never reads full, nor gives a percentage
		{aggregator.PlayProgress{Done: 4, Total: 4}, playBarWidth - 1, ""},
		{aggregator.PlayProgress{Done: 0, Total: 0, Exact: true}, 0, ""},
	}
	for _, c := range cases {
		bar := playBar(plainStyles(), c.p)
		if got := strings.Count(bar, barFill); got != c.filled {
			t.Errorf("%+v: %d filled cells, want %d", c.p, got, c.filled)
		}
		if w := len([]rune(bar)); w != playBarWidth {
			t.Errorf("%+v: bar is %d cells, want %d", c.p, w, playBarWidth)
		}
		if got := percent(c.p); got != c.percent {
			t.Errorf("%+v: percent %q, want %q", c.p, got, c.percent)
		}
	}
}

func TestLiveRepaint(t *testing.T) {
	var buf bytes.Buffer
	l := NewLive(&buf)
	l.Render([]string{"line-a", "line-b"})
	l.Render([]string{"line-c", "line-d"})

	out := buf.String()
	if !strings.Contains(out, ansiHideCursor) {
		t.Error("first render should hide the cursor")
	}
	if !strings.Contains(out, "\x1b[2A") {
		t.Error("second render should move the cursor up 2 lines")
	}
	if !strings.Contains(out, "line-c") || !strings.Contains(out, "line-d") {
		t.Error("repaint should contain the new frame")
	}
}

// vScreen replays a live block's ANSI (cursor-up, clear-line, newlines, text)
// onto a virtual screen, row to content: what the terminal would show.
func vScreen(out string) map[int]string {
	screen := map[int]string{}
	row := 0
	for i := 0; i < len(out); {
		switch {
		case strings.HasPrefix(out[i:], "\x1b["):
			j := i + 2
			for j < len(out) && !isLetter(out[j]) {
				j++
			}
			num := 0
			for k := i + 2; k < j && out[k] >= '0' && out[k] <= '9'; k++ {
				num = num*10 + int(out[k]-'0')
			}
			switch out[j] {
			case 'A':
				row -= num
			case 'K':
				screen[row] = ""
			}
			i = j + 1
		case out[i] == '\n':
			row++
			i++
		case out[i] == '\r':
			i++
		default:
			j := i
			for j < len(out) && out[j] != '\x1b' && out[j] != '\n' && out[j] != '\r' {
				j++
			}
			screen[row] = out[i:j]
			i = j
		}
	}
	return screen
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// TestLiveNoDriftOnHeightChange: the block grows and shrinks (the hosts line
// comes and goes between tasks) and must repaint in place, never stacking
// stale copies.
func TestLiveNoDriftOnHeightChange(t *testing.T) {
	var buf bytes.Buffer
	l := NewLive(&buf)
	for i := range 6 {
		f := []string{"HEADER", "a", "b"}
		if i%2 == 1 {
			f = append(f, "hosts")
		}
		l.Render(f)
	}
	headers := 0
	for _, line := range vScreen(buf.String()) {
		if line == "HEADER" {
			headers++
		}
	}
	if headers != 1 {
		t.Errorf("block drifted: %d HEADER lines on screen, want 1", headers)
	}
}

func TestLiveClearAndClose(t *testing.T) {
	var buf bytes.Buffer
	l := NewLive(&buf)
	l.Render([]string{"x", "y"})
	l.Clear()
	l.Close()
	for _, line := range vScreen(buf.String()) {
		if line != "" {
			t.Errorf("Clear left %q on screen", line)
		}
	}
	if !strings.Contains(buf.String(), ansiShowCursor) {
		t.Error("Close should restore the cursor")
	}
}
