// Package aggregator is the one stateful stage of the pipeline. It is fed the
// callback plugin's events and ansible-playbook's stderr lines, and keeps the
// run's state: plays with their task progress, the task running now and which
// hosts have reported for it, counters per host, changed and failed results,
// and grouped warnings. Snapshot exposes an immutable view for the renderer.
package aggregator

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// Aggregator accumulates run state. It is safe for concurrent use: the
// runner's reader goroutines feed it while the renderer takes snapshots.
type Aggregator struct {
	mu  sync.Mutex
	now func() time.Time

	file        string
	check, diff bool
	started     time.Time

	plays []*play
	tasks map[string]*task // by uuid, so results attribute even out of order
	cur   *current

	hosts map[string]*Counters

	changes     []Change
	failures    []Failure
	ignored     []Failure
	unreachable []Failure

	reconciled bool
	drift      []string

	warn warnBook
}

type play struct {
	name, uuid string
	hosts      []string
	total      int  // static count plus includes, handlers and serial batches
	exact      bool // false once a count arrived unknown
	done       int
	counted    map[string]bool // task uuids counted done in this batch
	out        map[string]bool // hosts that left the play: failed or unreachable
	start, end time.Time
	ended      bool
}

type task struct {
	name, role string
	handler    bool
	play       *play
}

type current struct {
	uuid     string
	task     *task
	started  time.Time
	reported []HostStatus
}

// New returns an empty Aggregator. now is the clock for elapsed times; nil
// means time.Now.
func New(now func() time.Time) *Aggregator {
	if now == nil {
		now = time.Now
	}
	return &Aggregator{
		now:   now,
		tasks: map[string]*task{},
		hosts: map[string]*Counters{},
		warn:  newWarnBook(),
	}
}

// Feed applies one event from the callback plugin.
func (a *Aggregator) Feed(ev events.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()

	at := eventTime(ev.Time)
	switch ev.Kind {
	case events.KindPlaybook:
		a.file, a.check, a.diff, a.started = ev.File, ev.Check, ev.Diff, at
	case events.KindPlay:
		a.startPlay(ev, at)
	case events.KindTask:
		a.startTask(ev, at)
	case events.KindInclude:
		if p := a.openPlay(); p != nil {
			p.grow(ev)
		}
	case events.KindResult:
		a.result(ev)
	case events.KindStats:
		a.endPlay(at)
		a.reconcile(ev.Recap)
	case events.KindPrompt:
		// the render's concern (issue 010): the counts don't move
	}
}

// Stderr scrapes one line of ansible-playbook's stderr for controller warnings.
func (a *Aggregator) Stderr(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.warn.stderr(line)
}

// Finish ends whatever is still open at at, for a run that stopped before its
// stats event, and closes a pending stderr warning.
func (a *Aggregator) Finish(at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.endPlay(at)
	a.warn.closeBlock()
}

func (a *Aggregator) openPlay() *play {
	if n := len(a.plays); n > 0 && !a.plays[n-1].ended {
		return a.plays[n-1]
	}
	return nil
}

func (a *Aggregator) startPlay(ev events.Event, at time.Time) {
	for _, h := range ev.Hosts {
		a.warn.hosts[h] = true
	}
	// serial: every batch announces the same play again and runs its task list again
	if p := a.openPlay(); p != nil && p.uuid == ev.UUID {
		a.endTask()
		p.grow(ev)
		p.hosts = slices.Clone(ev.Hosts)
		p.counted, p.out = map[string]bool{}, map[string]bool{}
		return
	}
	a.endPlay(at)
	p := &play{
		name: ev.Name, uuid: ev.UUID, hosts: slices.Clone(ev.Hosts), exact: true,
		counted: map[string]bool{}, out: map[string]bool{}, start: at,
	}
	p.grow(ev)
	a.plays = append(a.plays, p)
}

// grow adds an event's task count to the play's total, or marks it inexact.
func (p *play) grow(ev events.Event) {
	if n, ok := ev.TaskCount(); ok {
		p.total += n
	} else {
		p.exact = false
	}
}

func (a *Aggregator) endPlay(at time.Time) {
	a.endTask()
	if p := a.openPlay(); p != nil {
		p.end, p.ended = at, true
	}
}

func (a *Aggregator) startTask(ev events.Event, at time.Time) {
	p := a.openPlay()
	if p == nil { // a task outside any announced play; keep counting anyway
		p = &play{exact: false, counted: map[string]bool{}, out: map[string]bool{}, start: at}
		a.plays = append(a.plays, p)
	}
	a.endTask()
	t := &task{name: stripRole(ev.Name, ev.Role), role: ev.Role, handler: ev.Handler, play: p}
	a.tasks[ev.UUID] = t
	if ev.Handler && !p.counted[ev.UUID] {
		p.total++ // handlers are not in the static count
	}
	a.cur = &current{uuid: ev.UUID, task: t, started: at}
}

// endTask counts the current task done in its play, once per uuid per batch.
func (a *Aggregator) endTask() {
	if a.cur == nil {
		return
	}
	p := a.cur.task.play
	if !p.counted[a.cur.uuid] {
		p.counted[a.cur.uuid] = true
		p.done++
	}
	a.cur = nil
}

func (a *Aggregator) result(ev events.Event) {
	t := a.tasks[ev.UUID]
	if t == nil {
		t = &task{play: a.openPlay()}
	}
	c := a.hosts[ev.Host]
	if c == nil {
		c = &Counters{}
		a.hosts[ev.Host] = c
	}
	c.add(ev.Status, ev.Rescued)
	a.warn.hosts[ev.Host] = true

	if a.cur != nil && a.cur.uuid == ev.UUID {
		a.cur.reported = append(a.cur.reported, HostStatus{Host: ev.Host, Status: ev.Status})
	}

	playName := ""
	if t.play != nil {
		playName = t.play.name
	}
	f := Failure{Play: playName, Host: ev.Host, Task: t.name, Role: t.role, Msg: ev.Msg, Rescued: ev.Rescued}
	switch ev.Status {
	case events.StatusChanged:
		var diff *events.DiffStat
		if ev.DiffStat != nil {
			d := *ev.DiffStat
			diff = &d
		}
		a.changes = append(a.changes, Change{
			Play: playName, Host: ev.Host, Task: t.name, Role: t.role, Handler: t.handler, Diff: diff,
			DiffText: ev.DiffText,
		})
	case events.StatusFailed:
		a.failures = append(a.failures, f)
		if !ev.Rescued && t.play != nil {
			t.play.out[ev.Host] = true
		}
	case events.StatusIgnored:
		a.ignored = append(a.ignored, f)
	case events.StatusUnreachable:
		a.unreachable = append(a.unreachable, f)
		if t.play != nil {
			t.play.out[ev.Host] = true
		}
	case events.StatusOK, events.StatusSkipped:
	}

	for _, w := range ev.Warnings {
		a.warn.add(KindWarning, w, sourceResult, ev.Host)
	}
	for _, d := range ev.Deprecations {
		a.warn.add(KindDeprecation, d, sourceResult, ev.Host)
	}
}

// reconcile lets the final stats win per host. Ansible's recap buckets overlap
// (a changed or ignored result also counts as ok), so they are made disjoint
// first. The changes and failures lists stay as the results built them.
func (a *Aggregator) reconcile(recap map[string]events.HostStats) {
	if recap == nil {
		return
	}
	a.reconciled = true
	for h, s := range recap {
		want := Counters{
			OK:          max(0, s.OK-s.Changed-s.Ignored),
			Changed:     s.Changed,
			Skipped:     s.Skipped,
			Failed:      s.Failures,
			Ignored:     s.Ignored,
			Rescued:     s.Rescued,
			Unreachable: s.Unreachable,
		}
		got := a.hosts[h]
		if got != nil && *got == want {
			continue
		}
		a.hosts[h] = &want
		a.drift = append(a.drift, h)
	}
	slices.Sort(a.drift)
}

// stripRole drops the "role : " prefix ansible puts on a role task's name; the
// role is kept apart.
func stripRole(name, role string) string {
	if role == "" {
		return name
	}
	if rest, ok := strings.CutPrefix(name, role+" : "); ok {
		return rest
	}
	return name
}

func eventTime(t float64) time.Time {
	if t == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(t*float64(time.Second)))
}
