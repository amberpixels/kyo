// Package source picks where a run's events come from: kyo's callback plugin
// on fd 3, or, when the plugin stays silent, the default callback's text on
// stdout. Exactly one source feeds the sink, so nothing is counted twice.
//
// The plugin writes its first event before ansible prints the first PLAY
// banner, but the two pipes are read by separate goroutines, so the banner can
// arrive first. Text events are therefore held for a grace period after the
// banner and dropped if the plugin speaks within it.
package source

import (
	"sync"
	"time"

	"github.com/amberpixels/kyo/internal/events"
	"github.com/amberpixels/kyo/internal/textparse"
)

// DefaultGrace is how long text events wait for the plugin after the first
// PLAY banner.
const DefaultGrace = time.Second

// Source names who fed the run.
type Source int

const (
	None      Source = iota // neither: no plugin events and no default-callback text
	Plugin                  // the callback plugin
	Text                    // the plugin sent nothing by the first PLAY banner
	TextForce               // KYO_NO_PLUGIN: text from the start
)

// IsText reports whether the run was parsed from the default callback's text.
func (s Source) IsText() bool { return s == Text || s == TextForce }

// Options configures a Switch.
type Options struct {
	Sink      func(events.Event) // where the chosen source's events go
	Playbook  textparse.Playbook // the text doesn't print it
	Now       func() time.Time   // stamps text events; nil means time.Now
	Grace     time.Duration      // 0 means DefaultGrace
	ForceText bool               // ignore the plugin; parse text from the start

	// AfterFunc schedules the grace check; nil means time.AfterFunc. Tests
	// swap in a manual trigger.
	AfterFunc func(time.Duration, func()) (stop func() bool)
}

// Switch arbitrates between the two sources. Its methods are safe for
// concurrent use, and it never waits on its callers' locks.
type Switch struct {
	mu      sync.Mutex
	opt     Options
	parser  *textparse.Parser
	src     Source
	decided bool
	buf     []events.Event
	stop    func() bool // the pending grace check, nil when none
	dropped int
}

// New returns an undecided Switch, or a text one with ForceText.
func New(opt Options) *Switch {
	if opt.Grace == 0 {
		opt.Grace = DefaultGrace
	}
	if opt.AfterFunc == nil {
		opt.AfterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	s := &Switch{opt: opt}
	s.parser = textparse.New(opt.Playbook, opt.Now, s.textEvent)
	if opt.ForceText {
		s.src, s.decided = TextForce, true
	}
	return s
}

// Event takes one plugin event.
func (s *Switch) Event(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.decided {
		s.decide(Plugin)
	}
	if s.src != Plugin {
		s.dropped++
		return
	}
	s.opt.Sink(ev)
}

// Stdout takes one line of ansible-playbook's stdout.
func (s *Switch) Stdout(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.decided && !s.src.IsText() {
		return
	}
	played := s.parser.Played()
	s.parser.Line(line)
	if !s.decided && !played && s.parser.Played() {
		s.stop = s.opt.AfterFunc(s.opt.Grace, s.graceOver)
	}
}

// Finish ends the run: an undecided switch that saw a PLAY banner takes the
// text, and the parser's last events are sent. It returns the source, None
// when the text never showed a PLAY banner, KYO_NO_PLUGIN or not.
func (s *Switch) Finish() Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.decided && s.parser.Played() {
		s.decide(Text)
	}
	if s.src.IsText() {
		s.parser.Finish()
		if !s.parser.Played() {
			s.src = None
		}
	}
	return s.src
}

// Source returns the source chosen so far (None while undecided).
func (s *Switch) Source() Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src
}

// Dropped counts plugin events that arrived after the text was chosen.
func (s *Switch) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *Switch) graceOver() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.decided {
		s.decide(Text)
	}
}

// decide fixes the source; s.mu is held.
func (s *Switch) decide(src Source) {
	s.src, s.decided = src, true
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	buf := s.buf
	s.buf = nil
	if src.IsText() {
		for _, ev := range buf {
			s.opt.Sink(ev)
		}
	}
}

// textEvent receives the parser's events; s.mu is held.
func (s *Switch) textEvent(ev events.Event) {
	switch {
	case !s.decided:
		s.buf = append(s.buf, ev)
	case s.src.IsText():
		s.opt.Sink(ev)
	}
}
