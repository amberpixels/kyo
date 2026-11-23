// Package recording writes and reads a run's recording: the events the
// aggregator was fed, the stderr lines it scraped, and two kyo records around
// them (how the run started and how it ended), one JSON line each. A replay
// feeds the same items to a fresh aggregator, so it needs neither ansible nor
// the plugin.
package recording

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// Version is the format version kyo writes on its own records. A reader
// refuses a recording with a newer one.
const Version = 1

// kyo's own record kinds. The prefix keeps them out of the plugin's event
// namespace.
const (
	kindStart  = "kyo.start"
	kindStderr = "kyo.stderr"
	kindEnd    = "kyo.end"
)

// maxLine matches the runner's line limit: big diffs make long events.
const maxLine = 4 * 1024 * 1024

// Start is how the run began.
type Start struct {
	Time    time.Time
	Check   bool   // --check in the args, the summary's fallback without a playbook event
	LogPath string // the raw log the run wrote
}

// End is how the run ended.
type End struct {
	Time     time.Time
	ExitCode int
	Signaled bool
	Source   string // where the events came from: plugin, text, text-force or none
	NoPlugin bool   // KYO_NO_PLUGIN was set
	Late     int    // plugin events dropped after the text was chosen
}

// Item is one recorded input of the aggregator: an event, or a stderr line
// when Event is nil.
type Item struct {
	Time  time.Time
	Event *events.Event
	Line  string
}

// Recording is a read recording.
type Recording struct {
	Start Start
	Items []Item
	End   End
	Ended bool // the end record was there; without it the exit code is unknown
	Bad   int  // lines that did not decode
}

// wireRecord is the union of kyo's own records, and enough of an event to
// tell its kind and time.
type wireRecord struct {
	V        int     `json:"v"`
	Kind     string  `json:"ev"`
	Time     float64 `json:"t"`
	Check    bool    `json:"check,omitempty"`
	LogPath  string  `json:"log,omitempty"`
	Line     string  `json:"line,omitempty"`
	ExitCode int     `json:"code,omitempty"`
	Signaled bool    `json:"signaled,omitempty"`
	Source   string  `json:"source,omitempty"`
	NoPlugin bool    `json:"no_plugin,omitempty"`
	Late     int     `json:"late,omitempty"`
}

// Writer appends records to w. It is not safe for concurrent use, and its
// methods do nothing on a nil Writer, so a run that records nothing needs no
// checks. A write error is kept, later writes are dropped, and Close reports
// it: a full disk must not break the run.
type Writer struct {
	w   *bufio.Writer
	c   io.Closer // the file Create opened; nil for NewWriter
	err error
}

// NewWriter returns a Writer to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: bufio.NewWriter(w)} }

// Create returns a Writer to a new file at path, which Close closes.
func Create(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Writer{w: bufio.NewWriter(f), c: f}, nil
}

// Start writes the start record.
func (w *Writer) Start(s Start) {
	w.record(wireRecord{V: Version, Kind: kindStart, Time: unix(s.Time), Check: s.Check, LogPath: s.LogPath})
}

// Event writes one event as the plugin would.
func (w *Writer) Event(ev events.Event) {
	if w == nil || w.err != nil {
		return
	}
	b, err := events.Encode(ev)
	if err != nil {
		w.err = err
		return
	}
	w.line(b)
}

// Stderr writes one stderr line, stamped at.
func (w *Writer) Stderr(at time.Time, line string) {
	w.record(wireRecord{V: Version, Kind: kindStderr, Time: unix(at), Line: line})
}

// End writes the end record.
func (w *Writer) End(e End) {
	w.record(wireRecord{
		V: Version, Kind: kindEnd, Time: unix(e.Time), ExitCode: e.ExitCode, Signaled: e.Signaled,
		Source: e.Source, NoPlugin: e.NoPlugin, Late: e.Late,
	})
}

// Close flushes what is buffered, closes a file Create opened, and returns the
// first error.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	if w.err == nil {
		w.err = w.w.Flush()
	}
	if w.c != nil {
		if err := w.c.Close(); w.err == nil {
			w.err = err
		}
		w.c = nil
	}
	return w.err
}

func (w *Writer) record(r wireRecord) {
	if w == nil || w.err != nil {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		w.err = err
		return
	}
	w.line(b)
}

func (w *Writer) line(b []byte) {
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		w.err = err
	}
}

// ErrNewer is returned for a recording a newer kyo wrote.
var ErrNewer = errors.New("recorded by a newer kyo")

// Read reads a recording. A plain capture of the plugin's events, with no kyo
// records, reads too. A recording cut short (kyo was killed) reads with Ended
// false and its end time at its last item.
func Read(r io.Reader) (*Recording, error) {
	rec := &Recording{}
	var last time.Time
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var k wireRecord
		if err := json.Unmarshal(line, &k); err != nil {
			rec.Bad++
			continue
		}
		at := fromUnix(k.Time)
		switch k.Kind {
		case kindStart:
			if k.V > Version {
				return nil, fmt.Errorf("%w (format v%d, this kyo reads v%d)", ErrNewer, k.V, Version)
			}
			rec.Start = Start{Time: at, Check: k.Check, LogPath: k.LogPath}
			last = at
			continue
		case kindEnd:
			rec.End = End{
				Time: at, ExitCode: k.ExitCode, Signaled: k.Signaled,
				Source: k.Source, NoPlugin: k.NoPlugin, Late: k.Late,
			}
			rec.Ended = true
			continue
		}
		// An item with no time keeps the one before it, so pacing never jumps
		// back to the epoch.
		if at.IsZero() {
			at = last
		}
		last = at
		switch k.Kind {
		case kindStderr:
			rec.Items = append(rec.Items, Item{Time: at, Line: k.Line})
		default:
			ev, err := events.Decode(line)
			if err != nil {
				rec.Bad++
				continue
			}
			rec.Items = append(rec.Items, Item{Time: at, Event: &ev})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading recording: %w", err)
	}
	if rec.Start.Time.IsZero() {
		for _, it := range rec.Items {
			if !it.Time.IsZero() {
				rec.Start.Time = it.Time
				break
			}
		}
	}
	if !rec.Ended {
		rec.End.Time = last
	}
	if rec.End.Time.Before(rec.Start.Time) {
		rec.End.Time = rec.Start.Time
	}
	return rec, nil
}

// unix is t in the plugin's unit: float unix seconds, 0 for the zero time.
func unix(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixNano()) / float64(time.Second)
}

func fromUnix(t float64) time.Time {
	if t == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(t*float64(time.Second)))
}
