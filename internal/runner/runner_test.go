package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/events"
)

// TestRunRealPlaybook drives the real ansible-playbook with the real plugin.
// It skips when ansible isn't installed.
func TestRunRealPlaybook(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	pluginDir, err := callback.Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	playbook, _ := filepath.Abs("../../testdata/playbooks/basic.yml")

	var log bytes.Buffer
	var evs []events.Event
	res, err := Run(context.Background(), Options{
		Args: []string{"-i", "localhost,", "-c", "local", "--diff", playbook},
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"KYO_TEST_DIR=" + work,
			"ANSIBLE_CALLBACK_PLUGINS=" + pluginDir,
			"ANSIBLE_LOCALHOST_WARNING=false",
			"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
		},
		Stdin:   strings.NewReader(""),
		RawLog:  &log,
		OnEvent: func(ev events.Event) { evs = append(evs, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.BadEvents != 0 {
		t.Fatalf("exit %d, bad events %d\nlog:\n%s", res.ExitCode, res.BadEvents, log.String())
	}

	statuses := map[events.Status]int{}
	var play, stats *events.Event
	var sawInclude bool
	for i, ev := range evs {
		switch ev.Kind {
		case events.KindPlay:
			play = &evs[i]
		case events.KindStats:
			stats = &evs[i]
		case events.KindInclude:
			sawInclude = true
		case events.KindResult:
			statuses[ev.Status]++
			if ev.Status == events.StatusChanged && (ev.DiffStat == nil || ev.DiffStat.Added != 1) {
				t.Errorf("changed result diff stat %+v, want 1 added line", ev.DiffStat)
			}
			if strings.Contains(ev.Msg, "do-not-leak") {
				t.Error("no_log result leaked its message")
			}
		case events.KindPlaybook, events.KindTask, events.KindPrompt:
		}
	}

	if evs[0].Kind != events.KindPlaybook || evs[0].File != "basic.yml" || !evs[0].Diff || evs[0].Check {
		t.Errorf("first event %+v", evs[0])
	}
	if play == nil || play.Tasks != 6 {
		t.Errorf("play event %+v, want 6 static tasks", play)
	}
	if !sawInclude {
		t.Error("no include event")
	}
	if statuses[events.StatusChanged] != 1 || statuses[events.StatusSkipped] != 1 ||
		statuses[events.StatusIgnored] != 1 {
		t.Errorf("statuses %v", statuses)
	}
	if stats == nil || stats.Recap["localhost"].Changed != 1 || stats.Recap["localhost"].Ignored != 1 {
		t.Errorf("stats %+v", stats)
	}
	if !strings.Contains(log.String(), "PLAY RECAP") {
		t.Error("raw log is missing the default callback's output")
	}
}
