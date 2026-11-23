package engine

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/events"
	"github.com/amberpixels/kyo/internal/recording"
	"github.com/amberpixels/kyo/internal/render"
)

// fixtureRecording writes a recording of the aggregator's basic fixture plus
// its stderr warnings, ending with code, and returns its path.
func fixtureRecording(t *testing.T, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kyo-1.events.jsonl")
	w, err := recording.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	evs := readFixture(t, "basic.jsonl")
	start := time.Unix(0, int64(evs[0].Time*float64(time.Second)))
	w.Start(recording.Start{Time: start, Check: true, LogPath: "kyo-1.log"})
	for _, ev := range evs {
		w.Event(ev)
	}
	for _, line := range readLines(t, "warnings.stderr") {
		w.Stderr(start, line)
	}
	w.End(recording.End{Time: start.Add(5 * time.Second), ExitCode: code, Source: "plugin"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func readLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "aggregator", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func readFixture(t *testing.T, name string) []events.Event {
	t.Helper()
	var evs []events.Event
	for _, line := range readLines(t, name) {
		ev, err := events.Decode([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// TestReplaySummary: an instant replay ends with the summary the aggregator
// gives for the same inputs, recorded stderr warnings included, with the
// recorded log path and elapsed time, and exits with the recorded code.
func TestReplaySummary(t *testing.T) {
	path := fixtureRecording(t, 2)
	var stderr bytes.Buffer
	code, err := Replay(context.Background(), path, Options{
		Env: []string{"KYO_REPLAY_SECONDS=0"}, Stderr: &stderr,
	})
	if err != nil || code != 2 {
		t.Fatalf("code %d, err %v, stderr:\n%s", code, err, stderr.String())
	}

	agg := aggregator.New(nil)
	evs := readFixture(t, "basic.jsonl")
	for _, ev := range evs {
		agg.Feed(ev)
	}
	for _, line := range readLines(t, "warnings.stderr") {
		agg.Stderr(line)
	}
	start := time.Unix(0, int64(evs[0].Time*float64(time.Second)))
	agg.Finish(start.Add(5 * time.Second))
	var want bytes.Buffer
	render.Summary(&want, agg.Snapshot(), render.SummaryOpts{
		LogPath: "kyo-1.log", Elapsed: 5 * time.Second, ArgsCheck: true,
		Styles: render.NewStyles(&want, []string{"KYO_REPLAY_SECONDS=0"}),
	})
	// Off a terminal the view is plain milestones, so the summary comes last.
	if !strings.HasSuffix(stderr.String(), want.String()) {
		t.Errorf("replay summary:\n%s\nwant:\n%s", stderr.String(), want.String())
	}
	if !strings.Contains(stderr.String(), "Could not match supplied host pattern") {
		t.Errorf("the recorded stderr warnings are missing:\n%s", stderr.String())
	}
}

func TestReplayCutShort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cut.events.jsonl")
	write(t, path, `{"v":1,"ev":"kyo.start","t":10,"log":"kyo-1.log"}
{"v":1,"ev":"playbook","t":10,"file":"site.yml"}
{"v":1,"ev":"play","t":11,"name":"P","uuid":"p","hosts":["web-1"]}
`)
	var stderr bytes.Buffer
	code, err := Replay(context.Background(), path, Options{Env: []string{"KYO_REPLAY_SECONDS=0"}, Stderr: &stderr})
	if err != nil || code != 1 || !strings.Contains(stderr.String(), "exit code is unknown") {
		t.Fatalf("code %d, err %v, stderr:\n%s", code, err, stderr.String())
	}
}

func TestReplayErrors(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.events.jsonl")
	write(t, empty, `{"v":1,"ev":"kyo.start","t":10}`+"\n")
	cases := map[string]struct {
		path string
		env  []string
		code int
	}{
		"missing file":  {filepath.Join(t.TempDir(), "nope"), nil, 1},
		"no events":     {empty, nil, 1},
		"bad seconds":   {empty, []string{"KYO_REPLAY_SECONDS=soon"}, 2},
		"negative secs": {empty, []string{"KYO_REPLAY_SECONDS=-1"}, 2},
	}
	for name, c := range cases {
		code, err := Replay(context.Background(), c.path, Options{Env: c.env, Stderr: &bytes.Buffer{}})
		if err == nil || code != c.code {
			t.Errorf("%s: code %d, err %v", name, code, err)
		}
	}
}

func TestPace(t *testing.T) {
	start := time.Unix(100, 0)
	wall := time.Unix(5_000, 0)
	p := newPace(start, start.Add(10*time.Minute), 12*time.Second, wall)

	for rec, want := range map[time.Duration]time.Duration{
		0:                0,
		5 * time.Minute:  6 * time.Second,
		10 * time.Minute: 12 * time.Second,
		20 * time.Minute: 12 * time.Second, // past the end: clamped
		-time.Minute:     0,
	} {
		if got := p.due(start.Add(rec)); got != want {
			t.Errorf("due(+%v) = %v, want %v", rec, got, want)
		}
	}
	if got := p.clock(wall.Add(3 * time.Second)); !got.Equal(start.Add(150 * time.Second)) {
		t.Errorf("clock at 3s = %v, want start+2m30s", got.Sub(start))
	}
	if got := p.clock(wall.Add(time.Minute)); !got.Equal(start.Add(10 * time.Minute)) {
		t.Errorf("clock past the replay = %v, want the end", got.Sub(start))
	}

	instant := newPace(start, start.Add(time.Minute), 0, wall)
	if instant.due(start.Add(30*time.Second)) != 0 || !instant.clock(wall).Equal(start.Add(time.Minute)) {
		t.Error("a zero-length replay feeds everything at once and stands at the end")
	}
	zeroSpan := newPace(start, start, 12*time.Second, wall)
	if zeroSpan.due(start) != 0 {
		t.Error("a zero span is due at once")
	}
}

func TestRecordingPath(t *testing.T) {
	cases := map[string]string{
		"kyo-20261123-120000.log": "kyo-20261123-120000.events.jsonl",
		"/var/log/run.log":        "/var/log/run.events.jsonl",
		"run.txt":                 "run.txt.events.jsonl",
		os.DevNull:                "",
	}
	for in, want := range cases {
		if got := recordingPath(in); got != want {
			t.Errorf("recordingPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRunRecordsAndReplays: a real run's recording replays to the same
// summary and exit code.
func TestRunRecordsAndReplays(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "site.yml"), `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.command: "true"
      changed_when: true
    - ansible.builtin.debug: {msg: hi}
    - ansible.builtin.fail: {msg: boom}
`)
	logPath := filepath.Join(project, "run.log")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"KYO_LOG=" + logPath,
		"ANSIBLE_LOCALHOST_WARNING=false",
		"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
	}
	var live bytes.Buffer
	code, err := Run(context.Background(), Options{
		Args: []string{"-i", "localhost,", "-c", "local", "site.yml"},
		Env:  env, Dir: project, Stderr: &live, CacheRoot: t.TempDir(),
	})
	if err != nil || code == 0 {
		t.Fatalf("code %d, err %v, stderr:\n%s", code, err, live.String())
	}

	assertReplays(t, filepath.Join(project, "run.events.jsonl"), code, live.String())
}

// assertReplays replays the recording at path instantly and requires the
// run's exit code and output.
func assertReplays(t *testing.T, path string, code int, output string) {
	t.Helper()
	var replayed bytes.Buffer
	rcode, err := Replay(context.Background(), path, Options{
		Env: []string{"KYO_REPLAY_SECONDS=0"}, Stderr: &replayed,
	})
	if err != nil || rcode != code {
		t.Fatalf("replay code %d (run %d), err %v, stderr:\n%s", rcode, code, err, replayed.String())
	}
	if replayed.String() != output {
		t.Errorf("replay differs from the run\nrun:\n%s\nreplay:\n%s", output, replayed.String())
	}
}
