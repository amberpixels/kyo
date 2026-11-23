package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/events"
)

// fixtureRun is one real ansible-playbook run over the fixtures, with its
// result statuses indexed by task name and host.
type fixtureRun struct {
	res      *Result
	evs      []events.Event
	log      string
	statuses map[string]map[string]events.Status // task name -> host -> status
	dir      string                              // KYO_TEST_DIR
}

func (r *fixtureRun) first(kind events.Kind) *events.Event {
	for i := range r.evs {
		if r.evs[i].Kind == kind {
			return &r.evs[i]
		}
	}
	return nil
}

// requireAnsible skips the test when ansible-playbook is not on PATH, and logs
// its version so a failure names the ansible it ran against.
func requireAnsible(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	out, err := exec.Command("ansible-playbook", "--version").Output()
	if err == nil {
		t.Logf("%s", bytes.SplitN(out, []byte("\n"), 2)[0])
	}
}

// runFixture runs ansible-playbook with kyo's plugin and the given args, plus
// extra env entries.
func runFixture(t *testing.T, args []string, env ...string) *fixtureRun {
	t.Helper()
	pluginDir, err := callback.Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &fixtureRun{dir: t.TempDir(), statuses: map[string]map[string]events.Status{}}

	var log bytes.Buffer
	names := map[string]string{} // task uuid -> name
	res, err := Run(context.Background(), Options{
		Args: args,
		Env: append([]string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"KYO_TEST_DIR=" + r.dir,
			"ANSIBLE_CALLBACK_PLUGINS=" + pluginDir,
			"ANSIBLE_LOCALHOST_WARNING=false",
			"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
		}, env...),
		Stdin:  strings.NewReader(""),
		RawLog: &log,
		OnEvent: func(ev events.Event) {
			r.evs = append(r.evs, ev)
			switch ev.Kind {
			case events.KindTask:
				names[ev.UUID] = ev.Name
			case events.KindResult:
				name := names[ev.UUID]
				if r.statuses[name] == nil {
					r.statuses[name] = map[string]events.Status{}
				}
				r.statuses[name][ev.Host] = ev.Status
			case events.KindPlaybook, events.KindPlay, events.KindInclude, events.KindPrompt, events.KindStats:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.res, r.log = res, log.String()
	if res.BadEvents != 0 {
		t.Errorf("%d bad events\nlog:\n%s", res.BadEvents, r.log)
	}
	return r
}

func fixture(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestFixtures drives the real ansible-playbook with the real plugin over the
// fixture playbooks. It skips when ansible isn't installed. Statuses are
// checked per task and host, not as totals, because incidental events (include
// and handler bookkeeping) differ between ansible versions.
func TestFixtures(t *testing.T) {
	requireAnsible(t)
	inventory := fixture(t, "inventory.ini")
	both := func(s events.Status) map[string]events.Status {
		return map[string]events.Status{"web-1": s, "web-2": s}
	}

	cases := []struct {
		name     string
		playbook string
		args     []string
		exit     int
		want     map[string]map[string]events.Status
		check    func(t *testing.T, r *fixtureRun)
	}{
		{
			name:     "apply",
			playbook: "playbooks/basic.yml",
			args:     []string{"--diff"},
			want: map[string]map[string]events.Status{
				"Write a file":        both(events.StatusChanged),
				"Never runs":          both(events.StatusSkipped),
				"Loop":                both(events.StatusOK),
				"Fail but carry on":   both(events.StatusIgnored),
				"Secret":              both(events.StatusOK),
				"Included task":       both(events.StatusOK),
				"app : Configure app": both(events.StatusChanged),
				"app : Restart app":   both(events.StatusOK),
			},
			check: func(t *testing.T, r *fixtureRun) {
				if pb := r.first(events.KindPlaybook); pb == nil || pb.File != "basic.yml" || !pb.Diff || pb.Check {
					t.Errorf("playbook event %+v", pb)
				}
				play := r.first(events.KindPlay)
				if play == nil {
					t.Fatal("no play event")
				}
				if n, ok := play.TaskCount(); !ok || n != 7 || len(play.Hosts) != 2 {
					t.Errorf("play event %+v, want 7 static tasks on web-1 and web-2", play)
				}
				// included.yml counts its one task; include_role's count is unknown
				var includes []int
				for _, ev := range r.evs {
					if ev.Kind == events.KindInclude {
						n, ok := ev.TaskCount()
						if !ok {
							n = -1
						}
						includes = append(includes, n)
					}
				}
				if !slices.Equal(includes, []int{1, -1}) {
					t.Errorf("include task counts %v, want [1 -1]", includes)
				}
				var sawHandler bool
				for _, ev := range r.evs {
					if ev.Kind == events.KindTask && ev.Handler && ev.Name == "app : Restart app" {
						sawHandler = true
					}
					if ev.Kind == events.KindTask && ev.Name == "app : Configure app" && ev.Role != "app" {
						t.Errorf("role task %+v, want role app", ev)
					}
				}
				if !sawHandler {
					t.Error("no handler task event")
				}
				stats := r.first(events.KindStats)
				if stats == nil {
					t.Fatal("no stats event")
				}
				for _, h := range []string{"web-1", "web-2"} {
					if s := stats.Recap[h]; s.Changed != 2 || s.Ignored != 1 || s.Failures != 0 {
						t.Errorf("stats for %s: %+v", h, s)
					}
				}
				if _, err := os.Stat(filepath.Join(r.dir, "web-1.txt")); err != nil {
					t.Errorf("apply wrote nothing: %v", err)
				}
				if !strings.Contains(r.log, "PLAY RECAP") {
					t.Error("raw log is missing the default callback's output")
				}
			},
		},
		{
			name:     "check mode with diff",
			playbook: "playbooks/basic.yml",
			args:     []string{"--check", "--diff"},
			want: map[string]map[string]events.Status{
				"Write a file":        both(events.StatusChanged),
				"app : Configure app": both(events.StatusChanged),
			},
			check: func(t *testing.T, r *fixtureRun) {
				if pb := r.first(events.KindPlaybook); pb == nil || !pb.Check || !pb.Diff {
					t.Errorf("playbook event %+v, want check and diff", pb)
				}
				for _, ev := range r.evs {
					if ev.Kind == events.KindResult && ev.Status == events.StatusChanged &&
						(ev.DiffStat == nil || ev.DiffStat.Added != 1) {
						t.Errorf("changed result %+v diff stat %+v, want 1 added line", ev, ev.DiffStat)
					}
				}
				if entries, _ := os.ReadDir(r.dir); len(entries) != 0 {
					t.Errorf("check mode wrote %d files", len(entries))
				}
			},
		},
		{
			name:     "real failure",
			playbook: "playbooks/fail.yml",
			exit:     2,
			want: map[string]map[string]events.Status{
				"Break web-2":       {"web-1": events.StatusSkipped, "web-2": events.StatusFailed},
				"After the failure": {"web-1": events.StatusOK},
			},
			check: func(t *testing.T, r *fixtureRun) {
				if _, ran := r.statuses["After the failure"]["web-2"]; ran {
					t.Error("web-2 ran on after its failure")
				}
				stats := r.first(events.KindStats)
				if stats == nil || stats.Recap["web-2"].Failures != 1 || stats.Recap["web-1"].Failures != 0 {
					t.Errorf("stats %+v", stats)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"-i", inventory}, c.args...)
			r := runFixture(t, append(args, fixture(t, c.playbook)))
			if r.res.ExitCode != c.exit {
				t.Fatalf("exit %d, want %d\nlog:\n%s", r.res.ExitCode, c.exit, r.log)
			}
			for task, hosts := range c.want {
				for host, want := range hosts {
					if got := r.statuses[task][host]; got != want {
						t.Errorf("%q on %s: %q, want %q", task, host, got, want)
					}
				}
			}
			for _, ev := range r.evs {
				if strings.Contains(ev.Msg, "do-not-leak") {
					t.Error("no_log result leaked its message")
				}
			}
			if c.check != nil {
				c.check(t, r)
			}
		})
	}
}
