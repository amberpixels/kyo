package source

import (
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// harness is a Switch with a manual grace timer and a recording sink.
type harness struct {
	*Switch

	got   []events.Event
	fire  func()
	armed int
}

func newHarness(force bool) *harness {
	h := &harness{}
	h.Switch = New(Options{
		Sink:      func(ev events.Event) { h.got = append(h.got, ev) },
		Now:       func() time.Time { return time.Unix(100, 0) },
		ForceText: force,
		AfterFunc: func(_ time.Duration, f func()) func() bool {
			h.armed++
			h.fire = f
			return func() bool { h.fire = nil; return true }
		},
	})
	return h
}

func (h *harness) lines(ls ...string) {
	for _, l := range ls {
		h.Stdout(l)
	}
}

func (h *harness) kinds() []events.Kind {
	var ks []events.Kind
	for _, ev := range h.got {
		ks = append(ks, ev.Kind)
	}
	return ks
}

var plugin = events.Event{Kind: events.KindPlaybook, File: "from-plugin.yml"}

func TestPluginFirst(t *testing.T) {
	h := newHarness(false)
	h.Event(plugin)
	h.lines("PLAY [Base] ****", "TASK [One] ****", "ok: [web-1]")
	if src := h.Finish(); src != Plugin {
		t.Errorf("source %v", src)
	}
	if len(h.got) != 1 || h.got[0].File != "from-plugin.yml" || h.armed != 0 {
		t.Errorf("got %+v, armed %d: the text must not reach the sink", h.got, h.armed)
	}
}

func TestBannerThenPluginWithinGrace(t *testing.T) {
	h := newHarness(false)
	h.lines("PLAY [Base] ****", "TASK [One] ****")
	if h.armed != 1 || len(h.got) != 0 {
		t.Fatalf("armed %d, got %+v: text waits for the grace", h.armed, h.got)
	}
	h.Event(plugin)
	if h.fire != nil {
		t.Error("the grace check was not stopped")
	}
	h.lines("ok: [web-1]")
	if src := h.Finish(); src != Plugin || len(h.got) != 1 {
		t.Errorf("source %v, got %+v", src, h.got)
	}
}

func TestBannerThenSilence(t *testing.T) {
	h := newHarness(false)
	h.lines("PLAY [Base] ****", "TASK [One] ****", "ok: [web-1]")
	h.fire()
	if h.Source() != Text {
		t.Fatalf("source %v after the grace", h.Source())
	}
	want := []events.Kind{events.KindPlaybook, events.KindPlay, events.KindTask, events.KindResult}
	if got := h.kinds(); len(got) != len(want) {
		t.Fatalf("flushed %v, want %v", got, want)
	}
	h.Event(plugin) // late plugin events are dropped
	h.lines("PLAY RECAP ****", "web-1 : ok=1 changed=0 unreachable=0 failed=0")
	h.Finish()
	if h.Dropped() != 1 || h.got[len(h.got)-1].Kind != events.KindStats {
		t.Errorf("dropped %d, got %v", h.Dropped(), h.kinds())
	}
}

func TestFinishWhileUndecided(t *testing.T) {
	h := newHarness(false)
	h.lines("PLAY [Base] ****", "TASK [One] ****", "ok: [web-1]")
	if src := h.Finish(); src != Text || len(h.got) != 4 {
		t.Errorf("source %v, got %v", src, h.kinds())
	}
}

func TestForceText(t *testing.T) {
	h := newHarness(true)
	h.lines("PLAY [Base] ****")
	if h.armed != 0 || len(h.got) != 2 {
		t.Errorf("armed %d, got %v: forced text goes straight to the sink", h.armed, h.kinds())
	}
	if src := h.Finish(); src != TextForce {
		t.Errorf("source %v", src)
	}
}

func TestNeitherSource(t *testing.T) {
	h := newHarness(false)
	h.lines("web-1 | SUCCESS => {", "}")
	if src := h.Finish(); src != None || len(h.got) != 0 {
		t.Errorf("source %v, got %v", src, h.kinds())
	}
}
