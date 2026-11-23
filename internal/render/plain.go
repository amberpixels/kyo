package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
)

// plainInterval is how long plain mode stays quiet before it prints a
// progress line, when no play ended and nothing failed meanwhile.
const plainInterval = 15 * time.Second

// PlainProgress prints one line per milestone where a repainted block can't
// run (CI, agents, KYO_PLAIN, a non-terminal): the run's start, each finished
// play, each failure as it lands, and a progress line after plainInterval of
// silence. Never a line per result. Feed it snapshots on a ticker.
type PlainProgress struct {
	w         io.Writer
	st        *Styles
	argsCheck bool

	started     bool
	plays       int // finished plays already printed
	failures    int // s.Failures entries already looked at
	unreachable int
	lastEmit    time.Duration
}

// NewPlainProgress returns an emitter writing to w. argsCheck is --check in
// the args, for a start line printed before the playbook event.
func NewPlainProgress(w io.Writer, st *Styles, argsCheck bool) *PlainProgress {
	if st == nil {
		st = plainStyles()
	}
	return &PlainProgress{w: w, st: st, argsCheck: argsCheck}
}

// Update prints the milestones reached since the last call. elapsed is the
// wall time since the run started.
func (p *PlainProgress) Update(s aggregator.Snapshot, elapsed time.Duration) {
	if !p.started {
		if len(s.Plays) == 0 {
			return
		}
		p.started = true
		p.emit(elapsed, "%s · %s · %s", s.File, modeLabel(s, p.argsCheck), plural(hostCount(s), "host"))
	}

	for ; p.failures < len(s.Failures); p.failures++ {
		f := s.Failures[p.failures]
		if f.Rescued {
			continue
		}
		p.emit(elapsed, "%s %s · %s%s", p.st.failed.Render("✗"), f.Host,
			taskLabel(f.Role, f.Task, false), msgSuffix(f.Msg))
	}
	for ; p.unreachable < len(s.Unreachable); p.unreachable++ {
		f := s.Unreachable[p.unreachable]
		p.emit(elapsed, "%s %s unreachable · %s%s", p.st.failed.Render("!"), f.Host,
			taskLabel(f.Role, f.Task, false), msgSuffix(f.Msg))
	}

	for ; p.plays < len(s.Plays) && s.Plays[p.plays].Ended; p.plays++ {
		pl := s.Plays[p.plays]
		p.emit(elapsed, "%s %s %s in %s · %s", p.st.done.Render("✓"), playName(pl), progress(pl),
			clockText(pl.Took), countersShort(p.st, s.Totals))
	}

	if elapsed-p.lastEmit >= plainInterval {
		line := countersShort(p.st, s.Totals) + " · " + clockText(elapsed)
		if n := len(s.Plays); n > 0 && !s.Plays[n-1].Ended {
			pl := s.Plays[n-1]
			head := playName(pl) + " " + progress(pl)
			if s.Current != nil {
				head += " · " + taskLabel(s.Current.Role, s.Current.Name, s.Current.Handler)
			}
			line = head + " · " + line
		}
		p.emit(elapsed, "%s", line)
	}
}

func (p *PlainProgress) emit(elapsed time.Duration, format string, a ...any) {
	p.lastEmit = elapsed
	fmt.Fprintf(p.w, "kyo · "+format+"\n", a...)
}

// countersShort is the totals that matter mid-run, for a one-line form.
func countersShort(st *Styles, c aggregator.Counters) string {
	line := fmt.Sprintf("ok %d changed %d", c.OK, c.Changed)
	return line + " " + pick(c.Failed > 0, st.failed, st.dim).Render(fmt.Sprintf("failed %d", c.Failed))
}

// msgSuffix is ": <first line of msg>", or nothing for an empty message.
func msgSuffix(msg string) string {
	msg, _, _ = strings.Cut(strings.TrimSpace(msg), "\n")
	if msg == "" {
		return ""
	}
	return ": " + msg
}
