// Package engine wires a run together: install the callback plugin, point
// ansible at it without losing the user's plugin path, run the playbook with
// its output going to the raw log while the aggregator reads its events and
// stderr, and print a recap from the aggregator's snapshot. The live view and
// the summaries (issues 007, 008) read the same snapshot.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/ansiblecfg"
	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/runner"
)

// passthroughFlags make ansible-playbook print something and exit without
// running a play, so kyo has nothing to add.
var passthroughFlags = []string{
	"--list-tasks", "--list-hosts", "--list-tags", "--syntax-check",
	"--version", "-h", "--help",
}

// Options carries the process environment the engine needs, injectable for tests.
type Options struct {
	Args   []string
	Env    []string // nil means os.Environ()
	Dir    string   // empty means the current dir
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time

	CacheRoot string            // where the plugin is installed; empty means the user cache
	Lookup    ansiblecfg.Lookup // nil means ansible-config dump
}

// Run executes one wrapped ansible-playbook run and returns kyo's exit code.
func Run(ctx context.Context, opt Options) (int, error) {
	opt = withDefaults(opt)
	if wantPassthrough(opt.Args, opt.Env) {
		return passthrough(ctx, opt)
	}

	pluginDir, err := callback.Install(opt.CacheRoot)
	if err != nil {
		return 1, err
	}

	logPath := logDestination(opt.Env, opt.Now())
	logf, err := os.Create(logPath)
	if err != nil {
		return 1, fmt.Errorf("creating log %s: %w", logPath, err)
	}
	defer logf.Close()

	cbPath, lookupErr := ansiblecfg.CallbackPath(ctx, pluginDir, opt.Dir, opt.Env, opt.Lookup)
	if lookupErr != nil {
		// The run still works; only a custom callback_plugins dir may be missed.
		fmt.Fprintf(logf, "kyo: reading callback_plugins failed, using defaults: %v\n", lookupErr)
	}
	env := append(slices.Clone(opt.Env), ansiblecfg.EnvKey+"="+cbPath)

	agg := aggregator.New(opt.Now)
	start := opt.Now()
	res, err := runner.Run(ctx, runner.Options{
		Command:  getenv(opt.Env, "KYO_ANSIBLE_PLAYBOOK"),
		Args:     opt.Args,
		Dir:      opt.Dir,
		Env:      env,
		RawLog:   logf,
		OnEvent:  agg.Feed,
		OnStderr: agg.Stderr,
	})
	if err != nil {
		return 1, err
	}
	agg.Finish(opt.Now())

	printRecap(opt.Stderr, agg.Snapshot(), res, logPath, opt.Now().Sub(start))
	return res.ExitCode, nil
}

// printRecap is a one-screen stand-in until the plan/apply summary (issue 008).
func printRecap(w io.Writer, s aggregator.Snapshot, res *runner.Result, logPath string, elapsed time.Duration) {
	if res.Events == 0 {
		fmt.Fprintf(w, "kyo: no events from the callback plugin; the run's output is in %s\n", logPath)
		return
	}
	mode, verb := "apply", "changed"
	if s.Check {
		mode, verb = "plan", "would change"
	}

	hostsChanged := 0
	for _, h := range s.Hosts {
		if h.Changed > 0 {
			hostsChanged++
		}
	}
	fmt.Fprintf(w, "%s %s: %d %s on %d of %d hosts", mode, s.File, s.Totals.Changed, verb, hostsChanged, len(s.Hosts))
	if s.Totals.Failed > 0 {
		fmt.Fprintf(w, ", %d failed", s.Totals.Failed)
	}
	if s.Totals.Unreachable > 0 {
		fmt.Fprintf(w, ", %d unreachable", s.Totals.Unreachable)
	}
	fmt.Fprintf(w, "  (%s)\n", elapsed.Round(100*time.Millisecond))
	for _, h := range s.Hosts {
		fmt.Fprintf(w, "  %-24s ok %-4d changed %-4d skipped %-4d failed %-4d unreachable %-4d ignored %d\n",
			h.Host, h.OK, h.Changed, h.Skipped, h.Failed, h.Unreachable, h.Ignored)
	}
	if s.WarnTotal > 0 {
		fmt.Fprintf(w, "warnings %d (%d deprecation)\n", s.WarnTotal, s.Deprecations)
	}
	fmt.Fprintf(w, "raw log: %s\n", logPath)
}

func wantPassthrough(args, env []string) bool {
	if getenv(env, "KYO_PASSTHROUGH") == "1" {
		return true
	}
	for _, a := range args {
		if slices.Contains(passthroughFlags, a) {
			return true
		}
	}
	return false
}

// passthrough runs ansible-playbook untouched, inheriting stdio.
func passthrough(ctx context.Context, opt Options) (int, error) {
	command := getenv(opt.Env, "KYO_ANSIBLE_PLAYBOOK")
	if command == "" {
		command = "ansible-playbook"
	}
	cmd := exec.CommandContext(ctx, command, opt.Args...)
	cmd.Dir, cmd.Env = opt.Dir, opt.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, opt.Stdout, opt.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.ExitCode(), nil
	}
	return 1, err
}

func logDestination(env []string, now time.Time) string {
	if p := getenv(env, "KYO_LOG"); p != "" {
		return p
	}
	return "kyo-" + now.Format("20060102-150405") + ".log"
}

func withDefaults(opt Options) Options {
	if opt.Env == nil {
		opt.Env = os.Environ()
	}
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.CacheRoot == "" {
		opt.CacheRoot = callback.DefaultCacheRoot()
	}
	if opt.Lookup == nil {
		opt.Lookup = ansiblecfg.ConfigDump(getenv(opt.Env, "KYO_ANSIBLE_CONFIG"))
	}
	return opt
}

// getenv reads key from env, last value winning as exec does.
func getenv(env []string, key string) string {
	for _, e := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}
