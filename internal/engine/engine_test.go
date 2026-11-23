package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWantPassthrough(t *testing.T) {
	cases := []struct {
		args []string
		env  []string
		want bool
	}{
		{[]string{"site.yml", "--check"}, nil, false},
		{[]string{"site.yml", "--list-tasks"}, nil, true},
		{[]string{"--syntax-check", "site.yml"}, nil, true},
		{[]string{"site.yml"}, []string{"KYO_PASSTHROUGH=1"}, true},
		{[]string{"site.yml"}, []string{"KYO_PASSTHROUGH=0"}, false},
	}
	for _, c := range cases {
		if got := wantPassthrough(c.args, c.env); got != c.want {
			t.Errorf("wantPassthrough(%v, %v) = %v", c.args, c.env, got)
		}
	}
}

func TestArgsCheck(t *testing.T) {
	cases := map[string]bool{
		"site.yml --check":       true,
		"-C site.yml":            true,
		"-vvC site.yml":          true,
		"-CD site.yml":           true,
		"site.yml":               false,
		"-e C=1 site.yml":        false,
		"-eC=1 site.yml":         false,
		"-i hosts -l C site.yml": false,
		"site.yml -- --check":    false,
	}
	for args, want := range cases {
		if got := argsCheck(strings.Fields(args)); got != want {
			t.Errorf("argsCheck(%q) = %v, want %v", args, got, want)
		}
	}
}

// TestRunKeepsUsersCallbackPlugins is the acceptance test for issue 004: a
// callback_plugins dir from the project's ansible.cfg still loads next to kyo's.
func TestRunKeepsUsersCallbackPlugins(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "ansible.cfg"), "[defaults]\ncallback_plugins = ./plugins\n")
	write(t, filepath.Join(project, "plugins", "marker.py"), `from ansible.plugins.callback import CallbackBase
class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = 'aggregate'
    CALLBACK_NAME = 'marker'
    CALLBACK_NEEDS_ENABLED = False
    def v2_playbook_on_stats(self, stats):
        self._display.display('USER PLUGIN LOADED')
`)
	write(t, filepath.Join(project, "site.yml"), `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug: {msg: hi}
`)
	logPath := filepath.Join(project, "run.log")

	var stderr bytes.Buffer
	code, err := Run(context.Background(), Options{
		Args: []string{"-i", "localhost,", "-c", "local", "--check", "site.yml"},
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"KYO_LOG=" + logPath,
			"ANSIBLE_LOCALHOST_WARNING=false",
			"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
		},
		Dir:       project,
		Stderr:    &stderr,
		CacheRoot: t.TempDir(),
	})
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v, stderr:\n%s", code, err, stderr.String())
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "USER PLUGIN LOADED") {
		t.Errorf("user's callback plugin did not load; log:\n%s", log)
	}
	if !strings.HasPrefix(stderr.String(), "kyo · site.yml · plan (--check) · 1 host\n") ||
		!strings.Contains(stderr.String(), "  plan: no changes, nothing failed") ||
		!strings.Contains(stderr.String(), "raw log: "+logPath) {
		t.Errorf("summary:\n%s", stderr.String())
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlaybookFromArgs(t *testing.T) {
	pb := playbookFromArgs(
		[]string{"-i", "hosts.yml", "-e", "@vars.yml", "--check", "-D", "plays/site.yml", "other.yml"},
	)
	if pb.File != "site.yml" || !pb.Check || !pb.Diff {
		t.Errorf("%+v", pb)
	}
}

// TestRunNoPlugin is KYO_NO_PLUGIN=1: nothing installed or injected, and the
// recap comes from the default callback's text.
func TestRunNoPlugin(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "site.yml"), `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.command: "true"
      changed_when: true
    - ansible.builtin.debug:
        msg: "fd=[{{ lookup('env', 'KYO_FD') }}] cb=[{{ lookup('env', 'ANSIBLE_CALLBACK_PLUGINS') }}]"
`)
	logPath := filepath.Join(project, "run.log")
	cache := t.TempDir()

	var stderr bytes.Buffer
	code, err := Run(context.Background(), Options{
		Args: []string{"-i", "localhost,", "-c", "local", "site.yml"},
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"KYO_LOG=" + logPath,
			"KYO_NO_PLUGIN=1",
			"ANSIBLE_LOCALHOST_WARNING=false",
			"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
			"ANSIBLE_CALLBACK_RESULT_FORMAT=yaml",
		},
		Dir:       project,
		Stderr:    &stderr,
		CacheRoot: cache,
		Lookup: func(context.Context, string, []string) ([]string, error) {
			t.Error("callback_plugins looked up with KYO_NO_PLUGIN")
			return nil, nil
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v, stderr:\n%s", code, err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "apply: 1 changed on 1 host, nothing failed") ||
		!strings.Contains(stderr.String(), "KYO_NO_PLUGIN is set") {
		t.Errorf("summary:\n%s", stderr.String())
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Error("the plugin was installed")
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "fd=[] cb=[]") {
		t.Errorf("kyo's variables reached ansible:\n%s", log)
	}
}

// TestRunNoPluginOtherCallback: with KYO_NO_PLUGIN and a stdout callback whose
// text kyo doesn't parse, there is no view, so kyo says so instead of an empty
// summary.
func TestRunNoPluginOtherCallback(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not on PATH")
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "site.yml"), `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug: {msg: hi}
`)
	var stderr bytes.Buffer
	code, err := Run(context.Background(), Options{
		Args: []string{"-i", "localhost,", "-c", "local", "site.yml"},
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"KYO_LOG=" + filepath.Join(project, "run.log"),
			"KYO_NO_PLUGIN=1",
			"ANSIBLE_STDOUT_CALLBACK=minimal",
			"ANSIBLE_LOCALHOST_WARNING=false",
			"ANSIBLE_PYTHON_INTERPRETER=auto_silent",
		},
		Dir:    project,
		Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("code %d, err %v, stderr:\n%s", code, err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "KYO_NO_PLUGIN is set, and the output is not the default callback's") {
		t.Errorf("summary:\n%s", stderr.String())
	}
}
