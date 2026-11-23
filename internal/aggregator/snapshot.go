package aggregator

import (
	"slices"
	"strings"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// Counters are per-result outcomes. Unlike ansible's recap they are disjoint:
// every result lands in exactly one bucket, so ok excludes changed and ignored.
// Rescued comes from the plugin's rescue flag, then from the final stats.
type Counters struct {
	OK, Changed, Skipped, Failed, Ignored, Rescued, Unreachable int
}

func (c *Counters) add(s events.Status, rescued bool) {
	switch s {
	case events.StatusOK:
		c.OK++
	case events.StatusChanged:
		c.Changed++
	case events.StatusSkipped:
		c.Skipped++
	case events.StatusFailed:
		if rescued {
			c.Rescued++
		} else {
			c.Failed++
		}
	case events.StatusIgnored:
		c.Ignored++
	case events.StatusUnreachable:
		c.Unreachable++
	}
}

func (c *Counters) addAll(o Counters) {
	c.OK += o.OK
	c.Changed += o.Changed
	c.Skipped += o.Skipped
	c.Failed += o.Failed
	c.Ignored += o.Ignored
	c.Rescued += o.Rescued
	c.Unreachable += o.Unreachable
}

// PlayProgress is one play's row.
type PlayProgress struct {
	Name  string
	Done  int  // tasks finished
	Total int  // never below Done, plus one while a task of this play runs
	Exact bool // false when some count was unknown (include_role), so Total only grows as tasks start
	Hosts []string

	Started time.Time
	Took    time.Duration // set once Ended
	Ended   bool
}

// HostStatus is a host's outcome for the current task.
type HostStatus struct {
	Host   string
	Status events.Status
}

// CurrentTask is the task running now.
type CurrentTask struct {
	Play     string
	Name     string // without the role prefix
	Role     string
	Handler  bool
	Started  time.Time
	Elapsed  time.Duration
	Reported []HostStatus // in arrival order
	Pending  []string     // play hosts still due to report, sorted
}

// HostCounters is one host's counters.
type HostCounters struct {
	Counters

	Host string
}

// Change is a changed result (a would-change in check mode).
type Change struct {
	Play, Host, Task, Role string
	Handler                bool
	Diff                   *events.DiffStat // nil when the result carried no diff
	DiffText               string           // the diff itself, under --diff only
}

// Failure is a failed, ignored or unreachable result.
type Failure struct {
	Play, Host, Task, Role, Msg string
	Rescued                     bool // a rescue section caught it
}

// Snapshot is an immutable view of the run. It shares nothing with the
// Aggregator, so it can be read while events keep arriving.
type Snapshot struct {
	File    string
	Check   bool // --check: a plan
	Diff    bool
	Started time.Time

	Plays   []PlayProgress
	Current *CurrentTask // nil between tasks

	Totals Counters
	Hosts  []HostCounters // sorted by host

	Changes     []Change  // arrival order
	Failures    []Failure // failed results, rescued ones flagged
	Ignored     []Failure // failures with ignore_errors, kept apart
	Unreachable []Failure

	Warnings     []WarningGroup // most frequent first
	WarnTotal    int            // occurrences across all groups
	Deprecations int            // occurrences in deprecation groups

	Reconciled bool     // the final stats arrived
	Drift      []string // hosts whose counts the stats corrected
}

// Snapshot returns a consistent copy of the current state.
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := Snapshot{
		File: a.file, Check: a.check, Diff: a.diff, Started: a.started,
		Changes:     slices.Clone(a.changes),
		Failures:    slices.Clone(a.failures),
		Ignored:     slices.Clone(a.ignored),
		Unreachable: slices.Clone(a.unreachable),
		Reconciled:  a.reconciled,
		Drift:       slices.Clone(a.drift),
	}
	for i, c := range s.Changes {
		if c.Diff != nil {
			d := *c.Diff
			s.Changes[i].Diff = &d
		}
	}

	for _, p := range a.plays {
		pp := PlayProgress{
			Name: p.name, Done: p.done, Total: max(p.total, p.done), Exact: p.exact,
			Hosts: slices.Clone(p.hosts), Started: p.start, Ended: p.ended,
		}
		if a.cur != nil && a.cur.task.play == p && !p.counted[a.cur.uuid] {
			pp.Total = max(pp.Total, p.done+1)
		}
		if p.ended && !p.start.IsZero() {
			pp.Took = p.end.Sub(p.start)
		}
		s.Plays = append(s.Plays, pp)
	}

	if a.cur != nil {
		t := a.cur.task
		ct := &CurrentTask{
			Play: t.play.name, Name: t.name, Role: t.role, Handler: t.handler,
			Started: a.cur.started, Reported: slices.Clone(a.cur.reported),
		}
		if !a.cur.started.IsZero() {
			ct.Elapsed = a.now().Sub(a.cur.started)
		}
		reported := map[string]bool{}
		for _, r := range a.cur.reported {
			reported[r.Host] = true
		}
		for _, h := range t.play.hosts {
			if !reported[h] && !t.play.out[h] {
				ct.Pending = append(ct.Pending, h)
			}
		}
		slices.Sort(ct.Pending)
		s.Current = ct
	}

	for h, c := range a.hosts {
		s.Hosts = append(s.Hosts, HostCounters{Host: h, Counters: *c})
		s.Totals.addAll(*c)
	}
	slices.SortFunc(s.Hosts, func(x, y HostCounters) int { return strings.Compare(x.Host, y.Host) })

	s.Warnings, s.WarnTotal, s.Deprecations = a.warn.snapshot()
	return s
}
