package engine

import (
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// promptStall is how long stdout's unterminated tail may stand before kyo
// takes it for a prompt. Ansible writes whole lines everywhere else; input()
// leaves its prompt text without a newline while it waits.
const promptStall = 250 * time.Millisecond

// pauseActions name the pause module, which prompts with no callback event.
var pauseActions = []string{"pause", "ansible.builtin.pause", "ansible.legacy.pause"}

type gateKind int

const (
	gateClosed gateKind = iota
	gateVars            // a vars_prompt, from the plugin's prompt event
	gatePause           // a pause task, until its result
	gateStall           // a stdout tail that stood for promptStall
)

// promptGate steps the view aside while ansible waits for input. While it is
// open the view does not paint, and the prompt text ansible wrote to the
// captured stdout is copied to the terminal stream. Its mutex is held for
// every write to that stream, the view's paints included.
type promptGate struct {
	mu        sync.Mutex
	w         io.Writer
	kind      gateKind
	pauseUUID string
	pauseName string
	pausing   bool   // the pause's own prompt lines have begun
	tail      string // stdout's unterminated tail
	shown     int    // bytes of the current stdout line already written to w
	stall     func() bool
	stallGen  int

	onOpen    func() // takes the view down; called with mu held
	afterFunc func(time.Duration, func()) (stop func() bool)
}

func newPromptGate(w io.Writer) *promptGate {
	return &promptGate{
		w:         w,
		onOpen:    func() {},
		afterFunc: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
	}
}

// paint runs fn, a write to the terminal stream, unless a prompt is open.
func (g *promptGate) paint(fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.kind == gateClosed {
		fn()
	}
}

// observe reads one event from whichever source feeds the run.
func (g *promptGate) observe(ev events.Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch ev.Kind {
	case events.KindPrompt:
		g.open(gateVars)
	case events.KindTask:
		if slices.Contains(pauseActions, ev.Action) {
			g.close()
			g.open(gatePause)
			g.pauseUUID, g.pauseName = ev.UUID, strings.TrimSpace(ev.Name)
			return
		}
		g.close()
	case events.KindResult:
		if g.kind != gatePause || ev.UUID == g.pauseUUID {
			g.close()
		}
	case events.KindPlay, events.KindStats:
		g.close()
	case events.KindPlaybook, events.KindInclude:
	}
}

// line takes one complete stdout line. A pause prompts in whole lines, so its
// gate copies them from a prompt's first line ("[task name]", "Pausing for",
// or "Press 'C'" after a Ctrl+C) to its last (one ending in ":", "abort)" or
// "to abort"), each ending in \r\n because pause puts the terminal in raw
// mode. That leaves out the pause's own result line, which lands before its
// event. The other gates copy only the tail, so the next banner, which the
// stdout callback writes before the plugin's event, stays off the screen.
func (g *promptGate) line(line string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.kind == gatePause && !g.pausing {
		// Lines after an answer carry the cursor moves pause wrote first.
		g.pausing = strings.HasSuffix(line, "["+g.pauseName+"]") || strings.Contains(line, "Pausing for ") ||
			strings.Contains(line, "Press 'C' to continue")
	}
	switch {
	case g.kind == gatePause && g.pausing:
		_, _ = io.WriteString(g.w, line[min(g.shown, len(line)):]+"\r\n")
		g.pausing = !endsPrompt(line)
	case g.kind != gateClosed && g.shown > 0 && g.shown < len(line):
		_, _ = io.WriteString(g.w, line[g.shown:])
	}
	g.shown = 0
}

// pausePromptEnds are the endings of a pause's last prompt line.
var pausePromptEnds = []string{":", "abort)", "to abort"}

func endsPrompt(line string) bool {
	last := strings.TrimSpace(line)
	return slices.ContainsFunc(pausePromptEnds, func(end string) bool { return strings.HasSuffix(last, end) })
}

// partial takes stdout's unterminated tail, "" once a newline completed it.
func (g *promptGate) partial(tail string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tail = tail
	if tail == "" {
		g.cancelStall()
		return
	}
	if g.kind != gateClosed {
		g.showTail()
		return
	}
	if g.stall == nil {
		g.stallGen++
		gen := g.stallGen
		g.stall = g.afterFunc(promptStall, func() { g.stalled(gen) })
	}
}

func (g *promptGate) stalled(gen int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gen != g.stallGen || g.tail == "" {
		return
	}
	g.stall = nil
	g.open(gateStall)
}

// stop cancels a pending stall check, once the child's streams are closed.
func (g *promptGate) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancelStall()
	g.kind = gateClosed
}

// open opens the gate, or retags an open one; mu is held.
func (g *promptGate) open(kind gateKind) {
	g.cancelStall()
	if g.kind == gateClosed {
		g.onOpen()
	}
	g.kind = kind
	g.showTail()
}

// close lets the view paint again. The next paint starts on the current line
// with a line clear, so after Enter it lands on a fresh line and after a
// timed pause it overwrites the spent prompt. mu is held.
func (g *promptGate) close() {
	g.kind, g.pauseUUID, g.pauseName, g.pausing = gateClosed, "", "", false
}

// showTail writes the part of the tail not yet on screen; mu is held.
func (g *promptGate) showTail() {
	if g.shown < len(g.tail) {
		_, _ = io.WriteString(g.w, g.tail[g.shown:])
		g.shown = len(g.tail)
	}
}

func (g *promptGate) cancelStall() {
	if g.stall != nil {
		g.stall()
		g.stall = nil
	}
	g.stallGen++
}
