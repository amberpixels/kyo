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
	if !strings.Contains(stderr.String(), "plan site.yml: 0 would change on 0 of 1 hosts") {
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
