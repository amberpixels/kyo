package recording

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

func TestRoundTrip(t *testing.T) {
	t0 := time.Unix(1_000, 0)
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Start(Start{Time: t0, Check: true, LogPath: "kyo-1.log"})
	w.Event(events.Event{V: 1, Kind: events.KindPlaybook, Time: 1001, File: "site.yml", Check: true})
	w.Stderr(t0.Add(1500*time.Millisecond), "[WARNING]: something")
	w.Event(events.Event{
		V: 1, Kind: events.KindResult, Time: 1002, Host: "web-1", Status: events.StatusChanged,
		DiffStat: &events.DiffStat{Added: 2},
	})
	w.End(End{Time: t0.Add(3 * time.Second), ExitCode: 2, Source: "text", Late: 1})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	rec, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Start.Time.Equal(t0) || !rec.Start.Check || rec.Start.LogPath != "kyo-1.log" {
		t.Errorf("start %+v", rec.Start)
	}
	if !rec.Ended || rec.End.ExitCode != 2 || rec.End.Source != "text" || rec.End.Late != 1 ||
		!rec.End.Time.Equal(t0.Add(3*time.Second)) {
		t.Errorf("end %+v", rec.End)
	}
	if len(rec.Items) != 3 || rec.Bad != 0 {
		t.Fatalf("items %+v, bad %d", rec.Items, rec.Bad)
	}
	if ev := rec.Items[0].Event; ev == nil || ev.File != "site.yml" {
		t.Errorf("item 0 %+v", rec.Items[0])
	}
	if it := rec.Items[1]; it.Event != nil || it.Line != "[WARNING]: something" ||
		!it.Time.Equal(t0.Add(1500*time.Millisecond)) {
		t.Errorf("item 1 %+v", it)
	}
	if ev := rec.Items[2].Event; ev == nil || ev.DiffStat == nil || ev.DiffStat.Added != 2 {
		t.Errorf("item 2 %+v", rec.Items[2])
	}
}

// TestReadCutShort: kyo killed mid-run leaves no end record; the recording
// still reads, ending at its last item, and a broken line is counted.
func TestReadCutShort(t *testing.T) {
	in := `{"v":1,"ev":"kyo.start","t":10}
{"v":1,"ev":"play","t":11,"name":"P","uuid":"p"}
not json
{"v":1,"ev":"task","name":"T","uuid":"t"}
{"v":1,"ev":"kyo.stderr","t":14,"line":"x"}
`
	rec, err := Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Ended || rec.Bad != 1 || len(rec.Items) != 3 {
		t.Fatalf("%+v", rec)
	}
	if !rec.Items[1].Time.Equal(time.Unix(11, 0)) {
		t.Errorf("an item with no time keeps the previous one's: %v", rec.Items[1].Time)
	}
	if !rec.End.Time.Equal(time.Unix(14, 0)) {
		t.Errorf("end %v", rec.End.Time)
	}
}

func TestReadNewerVersion(t *testing.T) {
	_, err := Read(strings.NewReader(`{"v":99,"ev":"kyo.start","t":1}` + "\n"))
	if !errors.Is(err, ErrNewer) {
		t.Fatalf("err %v", err)
	}
}

// TestReadPluginCapture: a bare capture of the plugin's fd 3 replays too,
// starting at its first event.
func TestReadPluginCapture(t *testing.T) {
	f, err := os.Open("../aggregator/testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rec, err := Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Items) == 0 || rec.Bad != 0 || rec.Ended {
		t.Fatalf("items %d, bad %d, ended %v", len(rec.Items), rec.Bad, rec.Ended)
	}
	if rec.Start.Time.IsZero() || rec.End.Time.Before(rec.Start.Time) {
		t.Errorf("span %v .. %v", rec.Start.Time, rec.End.Time)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriterKeepsFirstError(t *testing.T) {
	w := NewWriter(failWriter{})
	w.Start(Start{Time: time.Unix(1, 0)})
	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err %v", err)
	}
	var nilW *Writer
	nilW.Event(events.Event{Kind: events.KindPlay})
	if err := nilW.Close(); err != nil {
		t.Fatal(err)
	}
}
