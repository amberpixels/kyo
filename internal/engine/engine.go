// Package engine wires a run together: install the callback plugin, point
// ansible at it without losing the user's plugin path, run the playbook with
// its output going to the raw log while the aggregator reads its events and
// stderr, show its progress (a repainted block on a terminal, milestone lines
// elsewhere), and print the plan or apply summary from the final snapshot.
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
	"sync"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/ansiblecfg"
	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/render"
	"github.com/amberpixels/kyo/internal/runner"
)

// Repaint cadence on a terminal, and how often plain mode checks for a
// milestone.
const (
	tickInterval  = 100 * time.Millisecond
	plainInterval = time.Second
)

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

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
	view := startView(opt, agg, start)
	defer view.close() // restore the cursor even on a panic; idempotent

	res, err := runner.Run(ctx, runner.Options{
		Command:  getenv(opt.Env, "KYO_ANSIBLE_PLAYBOOK"),
		Args:     opt.Args,
		Dir:      opt.Dir,
		Env:      env,
		RawLog:   logf,
		OnEvent:  agg.Feed,
		OnStderr: agg.Stderr,
	})
	view.stop()
	if err != nil {
		view.clear()
		return 1, err
	}
	agg.Finish(opt.Now())
	snap, elapsed := agg.Snapshot(), opt.Now().Sub(start)
	view.end(snap, elapsed)

	if res.Events == 0 {
		fmt.Fprintf(opt.Stderr, "kyo: no events from the callback plugin; the run's output is in %s\n", logPath)
		return res.ExitCode, nil
	}
	width, height := render.Size(opt.Stderr)
	if height == 0 {
		width = 0 // not a terminal: lines stay whole
	}
	render.Summary(opt.Stderr, snap, render.SummaryOpts{
		LogPath:   logPath,
		Elapsed:   elapsed,
		Width:     width,
		ArgsCheck: argsCheck(opt.Args),
		Styles:    render.NewStyles(opt.Stderr, opt.Env),
	})
	return res.ExitCode, nil
}

// view shows the run's progress while it runs: a block repainted in place on
// a terminal, or milestone lines in plain mode.
type view struct {
	w     io.Writer
	live  *render.Live          // nil in plain mode
	plain *render.PlainProgress // nil on a terminal
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

func startView(opt Options, agg *aggregator.Aggregator, start time.Time) *view {
	v := &view{w: opt.Stderr, done: make(chan struct{})}
	styles := render.NewStyles(opt.Stderr, opt.Env)
	check := argsCheck(opt.Args)

	if render.Plain(opt.Stderr, opt.Env) {
		v.plain = render.NewPlainProgress(opt.Stderr, styles, check)
		v.wg.Go(func() {
			t := time.NewTicker(plainInterval)
			defer t.Stop()
			for {
				select {
				case <-v.done:
					return
				case <-t.C:
					v.plain.Update(agg.Snapshot(), opt.Now().Sub(start))
				}
			}
		})
		return v
	}

	v.live = render.NewLive(opt.Stderr)
	fo := render.Opts{ArgsCheck: check, Styles: styles}
	v.wg.Go(func() {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-v.done:
				return
			case <-t.C:
				snap := agg.Snapshot()
				// Nothing is painted before the first play, so a vault or
				// become password prompt has the terminal to itself.
				if len(snap.Plays) == 0 {
					continue
				}
				fo.Spinner = spinner[i%len(spinner)]
				fo.Elapsed = opt.Now().Sub(start)
				// Measured every tick: the block has to fit the terminal as it
				// is now, and one row short of it, or each repaint scrolls the
				// screen and strands a copy of the block's top line.
				w, h := render.Size(opt.Stderr)
				fo.Width, fo.MaxLines = w, max(h-1, 0)
				v.live.Render(render.Frame(snap, fo))
			}
		}
	})
	return v
}

// stop halts the ticker; the view's writer is the caller's from then on.
func (v *view) stop() {
	v.once.Do(func() { close(v.done) })
	v.wg.Wait()
}

// end takes the view down for the summary: the block is cleared so the
// summary prints in its place, and plain mode prints its last milestones.
func (v *view) end(s aggregator.Snapshot, elapsed time.Duration) {
	if v.plain != nil {
		v.plain.Update(s, elapsed)
	}
	v.clear()
}

func (v *view) clear() {
	if v.live != nil {
		v.live.Clear()
		v.live.Close()
	}
}

// close restores the cursor. Safe after clear.
func (v *view) close() {
	if v.live != nil {
		v.live.Close()
	}
}

// argsCheck reports --check in ansible-playbook's args, the fallback for when
// the plugin's playbook event never came. A short cluster counts only when
// every letter is a flag that takes no value, so "-eC=1" is not a check.
func argsCheck(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--":
			return false
		case a == "--check":
			return true
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			flags := a[1:]
			if strings.ContainsRune(flags, 'C') && strings.Trim(flags, "vCDKkb") == "" {
				return true
			}
		}
	}
	return false
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
