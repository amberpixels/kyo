package aggregator

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// fixedNow is a clock one second after the hand-written fixtures' last event.
func fixedNow() time.Time { return time.Unix(108, 0) }

// feed replays a recorded or hand-written events fixture into a, stopping
// after limit lines when limit > 0.
func feed(t *testing.T, a *Aggregator, name string, limit int) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 0; sc.Scan() && (limit <= 0 || n < limit); n++ {
		ev, err := events.Decode(sc.Bytes())
		if err != nil {
			t.Fatalf("%s line %d: %v", name, n+1, err)
		}
		a.Feed(ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func stderrFixture(t *testing.T, a *Aggregator, name string) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		a.Stderr(sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestBasicRecorded(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "basic.jsonl", 0)
	s := a.Snapshot()

	if s.File != "basic.yml" || s.Check || !s.Diff {
		t.Errorf("playbook %q check %v diff %v", s.File, s.Check, s.Diff)
	}
	if len(s.Plays) != 1 {
		t.Fatalf("plays %+v", s.Plays)
	}
	p := s.Plays[0]
	// 7 static tasks, the included file's one, the app role's two (count
	// unknown, so the total grew as they ran) and its handler
	if p.Name != "Basic" || p.Done != 11 || p.Total != 11 || p.Exact || !p.Ended || p.Took <= 0 ||
		!slices.Equal(p.Hosts, []string{"web-1", "web-2"}) {
		t.Errorf("play %+v", p)
	}
	want := Counters{OK: 14, Changed: 4, Skipped: 2, Ignored: 2}
	if s.Totals != want {
		t.Errorf("totals %+v, want %+v", s.Totals, want)
	}
	if !s.Reconciled || len(s.Drift) != 0 {
		t.Errorf("reconciled %v drift %v: results and stats should agree", s.Reconciled, s.Drift)
	}
	if len(s.Changes) != 4 || s.Changes[0].Task != "Write a file" || s.Changes[0].Diff.Added != 1 ||
		s.Changes[3].Task != "Configure app" || s.Changes[3].Role != "app" {
		t.Errorf("changes %+v", s.Changes)
	}
	if len(s.Ignored) != 2 || s.Ignored[0].Msg != "expected failure" || len(s.Failures) != 0 {
		t.Errorf("ignored %+v failures %+v", s.Ignored, s.Failures)
	}
	if s.Current != nil {
		t.Errorf("no task runs after stats: %+v", s.Current)
	}
}

func TestFlowRecorded(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "flow.jsonl", 0)
	s := a.Snapshot()

	p := s.Plays[0]
	// 4 static, the app role's two (count unknown) and two handlers
	if p.Done != 8 || p.Total != 8 || p.Exact {
		t.Errorf("play %+v", p)
	}
	want := Counters{OK: 10, Changed: 4, Rescued: 2}
	if s.Totals != want || len(s.Drift) != 0 {
		t.Errorf("totals %+v drift %v, want %+v", s.Totals, s.Drift, want)
	}
	if len(s.Failures) != 2 || !s.Failures[0].Rescued || !s.Failures[1].Rescued {
		t.Errorf("failures %+v, want two rescued", s.Failures)
	}
	if len(s.Changes) != 4 {
		t.Fatalf("changes %+v", s.Changes)
	}
	loop, role := s.Changes[0], s.Changes[2]
	if loop.Task != "Write the configs" || loop.Diff == nil || loop.Diff.Added != 2 {
		t.Errorf("loop change %+v", loop)
	}
	if role.Task != "Configure app" || role.Role != "app" {
		t.Errorf("role change %+v: name must lose its role prefix", role)
	}
}

func TestTwoHosts(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "two-hosts.jsonl", 0)
	s := a.Snapshot()

	if !s.Check {
		t.Error("check flag lost")
	}
	if len(s.Plays) != 2 {
		t.Fatalf("plays %+v: a serial batch must not add a row", s.Plays)
	}
	base, rolling := s.Plays[0], s.Plays[1]
	// 3 static, an unknown include_role (2 tasks ran), a handler
	if base.Done != 6 || base.Total != 6 || base.Exact || base.Took != 4*time.Second {
		t.Errorf("base %+v", base)
	}
	// two batches of 2 tasks; the second batch's Deploy never started
	if rolling.Done != 3 || rolling.Total != 4 || !rolling.Exact || !slices.Equal(rolling.Hosts, []string{"web-2"}) {
		t.Errorf("rolling %+v", rolling)
	}

	// stats corrected web-2's ok from 2 to 3
	if !slices.Equal(s.Drift, []string{"web-2"}) {
		t.Errorf("drift %v", s.Drift)
	}
	want := map[string]Counters{
		"web-1": {OK: 1, Failed: 1, Unreachable: 1},
		"web-2": {OK: 3, Changed: 3, Skipped: 1, Ignored: 1},
	}
	for _, h := range s.Hosts {
		if h.Counters != want[h.Host] {
			t.Errorf("%s: %+v, want %+v", h.Host, h.Counters, want[h.Host])
		}
	}
	if s.Totals != (Counters{OK: 4, Changed: 3, Skipped: 1, Failed: 1, Ignored: 1, Unreachable: 1}) {
		t.Errorf("totals %+v", s.Totals)
	}

	if len(s.Changes) != 3 || s.Changes[1].Role != "nginx" || s.Changes[1].Task != "Write the site config" ||
		s.Changes[1].Diff.Added != 4 || !s.Changes[2].Handler || s.Changes[2].Diff != nil {
		t.Errorf("changes %+v", s.Changes)
	}
	if len(s.Failures) != 1 || s.Failures[0].Host != "web-1" || s.Failures[0].Play != "Base" ||
		s.Failures[0].Msg != "Destination directory does not exist" {
		t.Errorf("failures %+v", s.Failures)
	}
	if len(s.Ignored) != 1 || s.Ignored[0].Task != "Two" {
		t.Errorf("ignored %+v", s.Ignored)
	}
	if len(s.Unreachable) != 1 || s.Unreachable[0].Host != "web-1" || s.Unreachable[0].Play != "Rolling" {
		t.Errorf("unreachable %+v", s.Unreachable)
	}
}

func TestCurrentTaskMidRun(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "two-hosts.jsonl", 4) // playbook, play, task t1, web-1's result
	s := a.Snapshot()
	c := s.Current
	if c == nil || c.Name != "Install packages" || c.Play != "Base" {
		t.Fatalf("current %+v", c)
	}
	if len(c.Reported) != 1 || c.Reported[0] != (HostStatus{"web-1", events.StatusOK}) ||
		!slices.Equal(c.Pending, []string{"web-2"}) {
		t.Errorf("reported %+v pending %v", c.Reported, c.Pending)
	}
	if c.Elapsed != 108*time.Second-100100*time.Millisecond {
		t.Errorf("elapsed %v", c.Elapsed)
	}
	if p := s.Plays[0]; p.Done != 0 || p.Total != 3 || p.Ended {
		t.Errorf("play %+v", p)
	}

	// web-1 failed t2, so on the next task it is no longer pending
	a = New(fixedNow)
	feed(t, a, "two-hosts.jsonl", 9) // through task t3's start
	c = a.Snapshot().Current
	if c == nil || c.Name != "Include extras" || !slices.Equal(c.Pending, []string{"web-2"}) {
		t.Errorf("current %+v", c)
	}
}

func TestTotalNeverBelowRunningTask(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "two-hosts.jsonl", 14) // the role's second task runs, past the static 3
	p := a.Snapshot().Plays[0]
	if p.Done != 4 || p.Total != 5 || p.Exact {
		t.Errorf("play %+v, want 4 done of 5 while the fifth runs", p)
	}
}

func TestFinishWithoutStats(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "two-hosts.jsonl", 4)
	a.Finish(time.Unix(103, 0))
	s := a.Snapshot()
	if s.Current != nil || !s.Plays[0].Ended || s.Plays[0].Done != 1 || s.Plays[0].Took != 3*time.Second {
		t.Errorf("snapshot %+v", s.Plays)
	}
	if s.Reconciled {
		t.Error("no stats arrived")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	a := New(fixedNow)
	feed(t, a, "two-hosts.jsonl", 5)
	s := a.Snapshot()
	s.Changes[0].Diff.Added = 99
	s.Plays[0].Hosts[0] = "changed"
	again := a.Snapshot()
	if again.Changes[0].Diff.Added != 3 || again.Plays[0].Hosts[0] != "web-1" {
		t.Error("snapshot shares memory with the aggregator")
	}
}
