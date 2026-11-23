package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/events"
)

// TestSSHControlPersist runs a playbook over real SSH with a ControlPersist
// master that outlives the run, and checks Run still returns promptly: the
// master must hold neither the event pipe nor stdout/stderr open. It needs a
// reachable sshd, so it runs only when KYO_SSH_HOST is set (`just test-ssh`
// starts a throwaway one). KYO_SSH_PORT, KYO_SSH_USER and KYO_SSH_KEY are
// optional.
func TestSSHControlPersist(t *testing.T) {
	host := os.Getenv("KYO_SSH_HOST")
	if host == "" {
		t.Skip("KYO_SSH_HOST not set")
	}
	requireAnsible(t)

	// ControlPath is a unix socket (about 100 bytes max, and ssh appends a
	// temp suffix while binding); t.TempDir's path is too long on macOS.
	dir, err := os.MkdirTemp("", "kyo-ssh") //nolint:usetesting // socket path length
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sshOpts := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=120s",
		"-o", "ControlPath=" + filepath.Join(dir, "cm"),
		"-o", "UserKnownHostsFile=" + filepath.Join(dir, "known_hosts"),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
	}
	var target []string // the same connection, for ssh -O
	if p := os.Getenv("KYO_SSH_PORT"); p != "" {
		target = append(target, "-p", p)
	}
	if u := os.Getenv("KYO_SSH_USER"); u != "" {
		target = append(target, "-l", u)
	}
	if k := os.Getenv("KYO_SSH_KEY"); k != "" {
		sshOpts = append(sshOpts, "-o", "IdentitiesOnly=yes")
		target = append(target, "-i", k)
	}
	target = append(target, host)

	// raw needs no Python on the target, so the test does not depend on it.
	playbook := filepath.Join(dir, "site.yml")
	if err := os.WriteFile(playbook, []byte(`- hosts: all
  gather_facts: false
  tasks:
    - name: Over ssh
      ansible.builtin.raw: "true"
    - name: Again over the same master
      ansible.builtin.raw: "true"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-i", host + ",", "-c", "ssh"}
	if p := os.Getenv("KYO_SSH_PORT"); p != "" {
		args = append(args, "-e", "ansible_port="+p)
	}
	if u := os.Getenv("KYO_SSH_USER"); u != "" {
		args = append(args, "-u", u)
	}
	if k := os.Getenv("KYO_SSH_KEY"); k != "" {
		args = append(args, "--private-key", k)
	}
	args = append(args, playbook)

	pluginDir, err := callback.Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res   *Result
		err   error
		stats bool
	}
	done := make(chan outcome, 1)
	go func() {
		var o outcome
		o.res, o.err = Run(context.Background(), Options{
			Args: args,
			Env: []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + os.Getenv("HOME"),
				"ANSIBLE_CALLBACK_PLUGINS=" + pluginDir,
				"ANSIBLE_SSH_ARGS=" + strings.Join(sshOpts, " "),
			},
			Stdin: strings.NewReader(""),
			OnEvent: func(ev events.Event) {
				if ev.Kind == events.KindStats {
					o.stats = true
				}
			},
		})
		done <- o
	}()

	// Far inside the 120s persist window: a reader waiting on the master would
	// still be blocked here.
	var o outcome
	select {
	case o = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("Run did not return while the ControlPersist master was alive")
	}
	if o.err != nil || o.res.ExitCode != 0 || o.res.BadEvents != 0 || !o.stats {
		t.Fatalf("err %v, result %+v, stats event %v", o.err, o.res, o.stats)
	}

	// The master must have outlived the run, or the test proved nothing.
	ctl := append(append([]string{}, sshOpts...), target...)
	t.Cleanup(func() { _ = exec.Command("ssh", append([]string{"-O", "exit"}, ctl...)...).Run() })
	if out, err := exec.Command("ssh", append([]string{"-O", "check"}, ctl...)...).CombinedOutput(); err != nil {
		t.Fatalf("no ControlPersist master after the run: %v\n%s", err, out)
	}
}
