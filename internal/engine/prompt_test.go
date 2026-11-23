package engine

import (
	"bytes"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// testGate is a gate over a buffer whose stall timer fires by hand.
type testGate struct {
	*promptGate

	out    *bytes.Buffer
	opened int
	fire   func() // the pending stall check; nil when none
	waited time.Duration
}

func newTestGate() *testGate {
	tg := &testGate{out: &bytes.Buffer{}}
	tg.promptGate = newPromptGate(tg.out)
	tg.onOpen = func() { tg.opened++ }
	tg.afterFunc = func(d time.Duration, f func()) func() bool {
		tg.waited, tg.fire = d, f
		return func() bool { tg.fire = nil; return true }
	}
	return tg
}

// painted reports whether a paint goes through now.
func (tg *testGate) painted() bool {
	ok := false
	tg.paint(func() { ok = true })
	return ok
}

func TestGateVarsPrompt(t *testing.T) {
	g := newTestGate()
	g.partial("Your name: ") // the text can beat the event
	g.observe(events.Event{Kind: events.KindPrompt, Var: "name"})

	if g.opened != 1 || g.painted() {
		t.Fatalf("opened %d, painted %v; want the view down", g.opened, g.painted())
	}
	if got := g.out.String(); got != "Your name: " {
		t.Errorf("shown %q", got)
	}
	g.line("Your name: ")
	g.partial("")
	g.line("PLAY [second] ****") // the banner beats the play event
	g.observe(events.Event{Kind: events.KindPlay})

	if !g.painted() {
		t.Error("view still down after the play event")
	}
	if got := g.out.String(); got != "Your name: " {
		t.Errorf("shown %q; want the prompt once and no banner", got)
	}
}

func TestGatePauseCopiesLines(t *testing.T) {
	g := newTestGate()
	g.observe(events.Event{Kind: events.KindTask, Action: "ansible.builtin.pause", Name: "Wait", UUID: "p"})
	g.line("ok: [web-1]") // the last task's line, late on its pipe
	g.line("[Wait]")
	g.line("Press enter to continue, Ctrl+C to interrupt:")
	g.line("ok: [web-1]") // the pause's own result, ahead of its event
	g.observe(events.Event{Kind: events.KindResult, UUID: "other"})
	if g.painted() {
		t.Error("another task's result closed the pause")
	}
	g.observe(events.Event{Kind: events.KindResult, UUID: "p"})
	if !g.painted() {
		t.Error("the pause's result did not close it")
	}
	if want := "[Wait]\r\nPress enter to continue, Ctrl+C to interrupt:\r\n"; g.out.String() != want {
		t.Errorf("shown %q, want %q", g.out.String(), want)
	}
}

func TestGateStall(t *testing.T) {
	g := newTestGate()
	g.partial("Continue? ")
	if g.fire == nil || g.waited != promptStall {
		t.Fatalf("no stall check armed (waited %v)", g.waited)
	}
	if !g.painted() {
		t.Fatal("opened before the stall")
	}
	g.partial("Continue? [y") // growth keeps the first check
	g.fire()
	if g.painted() || g.out.String() != "Continue? [y" {
		t.Fatalf("after the stall: painted %v, shown %q", g.painted(), g.out.String())
	}
	g.partial("Continue? [y/n]: ")
	if got := g.out.String(); got != "Continue? [y/n]: " {
		t.Errorf("shown %q; want each byte once", got)
	}
	g.observe(events.Event{Kind: events.KindTask, Action: "debug"})
	if !g.painted() {
		t.Error("the next task did not close the stall")
	}
}

func TestGateLineInTimeOpensNothing(t *testing.T) {
	g := newTestGate()
	g.partial("half a li")
	g.line("half a line")
	g.partial("")
	if g.fire != nil {
		t.Error("the stall check outlived its line")
	}
	if !g.painted() || g.opened != 0 || g.out.Len() != 0 {
		t.Errorf("painted %v, opened %d, shown %q", g.painted(), g.opened, g.out.String())
	}
}
