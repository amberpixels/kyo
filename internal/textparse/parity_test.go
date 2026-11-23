package textparse_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/events"
	"github.com/amberpixels/kyo/internal/runner"
	"github.com/amberpixels/kyo/internal/textparse"
)

func clock() time.Time { return time.Unix(100, 0) }

// view is what both sources must agree on. Task totals, diff sizes, result
// warnings and loop summary messages exist only in the plugin's events.
type view struct {
	File        string
	Check, Diff bool
	Plays       []string
	Hosts       []aggregator.HostCounters
	Changes     []string
	Failures    []string
	Ignored     []string
	Unreachable []string
	Statuses    []string // task|host|status, per result event
}

func viewOf(evs []events.Event) view {
	a := aggregator.New(clock)
	names := map[string]string{}
	var statuses []string
	for _, ev := range evs {
		a.Feed(ev)
		switch ev.Kind {
		case events.KindTask:
			names[ev.UUID] = ev.Name
		case events.KindResult:
			statuses = append(statuses, names[ev.UUID]+"|"+ev.Host+"|"+string(ev.Status))
		case events.KindPlaybook, events.KindPlay, events.KindInclude, events.KindPrompt, events.KindStats:
		}
	}
	a.Finish(clock())
	s := a.Snapshot()

	v := view{File: s.File, Check: s.Check, Diff: s.Diff, Hosts: s.Hosts, Statuses: statuses}
	for _, p := range s.Plays {
		v.Plays = append(v.Plays, p.Name)
	}
	for _, c := range s.Changes {
		v.Changes = append(v.Changes, fmt.Sprintf("%s|%s|%s|%s|%v", c.Play, c.Host, c.Role, c.Task, c.Handler))
	}
	failures := func(fs []aggregator.Failure) []string {
		var out []string
		for _, f := range fs {
			out = append(out, fmt.Sprintf("%s|%s|%s|%s|%v|%s", f.Play, f.Host, f.Role, f.Task, f.Rescued, f.Msg))
		}
		return out
	}
	v.Failures, v.Ignored, v.Unreachable = failures(s.Failures), failures(s.Ignored), failures(s.Unreachable)
	// The text reports a loop's hosts when the task ends, the plugin as each
	// host finishes, so order within the lists is not compared.
	for _, l := range [][]string{v.Changes, v.Failures, v.Ignored, v.Unreachable, v.Statuses} {
		slices.Sort(l)
	}
	return v
}

func parseLines(pb textparse.Playbook, lines []string) []events.Event {
	var evs []events.Event
	p := textparse.New(pb, clock, func(ev events.Event) { evs = append(evs, ev) })
	for _, l := range lines {
		p.Line(l)
	}
	p.Finish()
	return evs
}

func compare(t *testing.T, plugin, text view) {
	t.Helper()
	if !cmpEqual(plugin, text) {
		t.Errorf("text source disagrees with the plugin\nplugin: %+v\ntext:   %+v", plugin, text)
	}
	if len(plugin.Statuses) == 0 {
		t.Error("no results at all")
	}
}

func cmpEqual(a, b view) bool {
	return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// TestRecordedParity replays each fixture's recorded plugin events and the
// default callback's stdout from the same run (and a yaml-format run) and
// expects the same view from both.
func TestRecordedParity(t *testing.T) {
	dir := filepath.Join("..", "aggregator", "testdata")
	for _, name := range []string{"basic", "flow", "fail"} {
		var evs []events.Event
		for _, l := range readLines(t, filepath.Join(dir, name+".jsonl")) {
			ev, err := events.Decode([]byte(l))
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, ev)
		}
		plugin := viewOf(evs)
		pb := textparse.Playbook{File: name + ".yml", Diff: true}
		for _, format := range []string{"stdout", "yaml.stdout"} {
			t.Run(name+"."+format, func(t *testing.T) {
				compare(t, plugin, viewOf(parseLines(pb, readLines(t, filepath.Join(dir, name+"."+format)))))
			})
		}
	}
}

// TestLiveParity runs each fixture through the real ansible-playbook with the
// plugin loaded and feeds the same run's events and stdout to two views, so
// every ansible-core in CI's matrix checks the parser against its own output.
func TestLiveParity(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	pluginDir, err := callback.Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inventory, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "inventory.ini"))

	cases := []struct {
		playbook string
		args     []string
		env      []string
	}{
		{playbook: "basic.yml", args: []string{"--diff"}},
		{
			playbook: "basic.yml",
			args:     []string{"--check", "--diff"},
			env:      []string{"ANSIBLE_CALLBACK_RESULT_FORMAT=yaml"},
		},
		{playbook: "flow.yml"},
		{playbook: "fail.yml", env: []string{"ANSIBLE_CALLBACK_RESULT_FORMAT=yaml"}},
	}
	for _, c := range cases {
		t.Run(strings.Join(append(append([]string{c.playbook}, c.args...), c.env...), " "), func(t *testing.T) {
			playbook, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "playbooks", c.playbook))
			args := append(append([]string{"-i", inventory}, c.args...), playbook)
			var (
				evs   []events.Event
				lines []string
				log   bytes.Buffer
			)
			_, err := runner.Run(context.Background(), runner.Options{
				Args: args,
				Env: append([]string{
					"PATH=" + os.Getenv("PATH"),
					"HOME=" + os.Getenv("HOME"),
					"KYO_TEST_DIR=" + t.TempDir(),
					"ANSIBLE_CALLBACK_PLUGINS=" + pluginDir,
					"ANSIBLE_LOCALHOST_WARNING=false",
					"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
				}, c.env...),
				Stdin:    strings.NewReader(""),
				RawLog:   &log,
				OnEvent:  func(ev events.Event) { evs = append(evs, ev) },
				OnStdout: func(l string) { lines = append(lines, l) },
			})
			if err != nil {
				t.Fatal(err)
			}
			pb := textparse.Playbook{
				File:  c.playbook,
				Check: slices.Contains(c.args, "--check"),
				Diff:  slices.Contains(c.args, "--diff"),
			}
			plugin, text := viewOf(evs), viewOf(parseLines(pb, lines))
			if !cmpEqual(plugin, text) {
				t.Logf("stdout:\n%s", log.String())
			}
			compare(t, plugin, text)
		})
	}
}
