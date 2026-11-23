// Package engine wires a run together: install the callback plugin, point
// ansible at it without losing the user's plugin path, run the playbook with
// its output going to the raw log while the aggregator reads its events (from
// the plugin, or parsed from the default callback's text when the plugin stays
// silent) and stderr, show its progress (a repainted block on a terminal,
// milestone lines elsewhere), and print the plan or apply summary from the
// final snapshot.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/ansiblecfg"
	"github.com/amberpixels/kyo/internal/callback"
	"github.com/amberpixels/kyo/internal/events"
	"github.com/amberpixels/kyo/internal/recording"
	"github.com/amberpixels/kyo/internal/render"
	"github.com/amberpixels/kyo/internal/runner"
	"github.com/amberpixels/kyo/internal/source"
	"github.com/amberpixels/kyo/internal/textparse"
)

// Repaint cadence on a terminal, and how often plain mode checks for a
// milestone.
const (
	tickInterval  = 100 * time.Millisecond
	plainInterval = time.Second
)

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// ExitChangesPending is kyo's exit code for a plan whose run succeeded with
// changes pending, under KYO_DETAILED_EXIT=1. ansible-playbook owns 0-5, 99
// and 250.
const ExitChangesPending = 10

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

	// KYO_NO_PLUGIN: inject nothing into ansible and read only its text.
	noPlugin := getenv(opt.Env, "KYO_NO_PLUGIN") == "1"

	var pluginDir string
	if !noPlugin {
		var err error
		if pluginDir, err = callback.Install(opt.CacheRoot); err != nil {
			return 1, err
		}
	}

	logPath := logDestination(opt.Env, opt.Now())
	logf, err := os.Create(logPath)
	if err != nil {
		return 1, fmt.Errorf("creating log %s: %w", logPath, err)
	}
	defer logf.Close()

	env := opt.Env
	if !noPlugin {
		cbPath, lookupErr := ansiblecfg.CallbackPath(ctx, pluginDir, opt.Dir, opt.Env, opt.Lookup)
		if lookupErr != nil {
			// The run still works; only a custom callback_plugins dir may be missed.
			fmt.Fprintf(logf, "kyo: reading callback_plugins failed, using defaults: %v\n", lookupErr)
		}
		env = append(slices.Clone(opt.Env), ansiblecfg.EnvKey+"="+cbPath)
	}

	agg := aggregator.New(opt.Now)
	rec := startRecording(logPath, logf)
	in := &inputs{agg: agg, rec: rec, now: opt.Now}
	gate := newPromptGate(opt.Stderr)
	sw := source.New(source.Options{
		Sink: func(ev events.Event) {
			in.Event(ev)
			gate.observe(ev)
		},
		Playbook:  playbookFromArgs(opt.Args),
		Now:       opt.Now,
		ForceText: noPlugin,
	})
	var onEvent func(events.Event)
	if !noPlugin {
		onEvent = sw.Event
	}

	start := opt.Now()
	check := argsCheck(opt.Args)
	rec.Start(recording.Start{Time: start, Check: check, LogPath: logPath})
	defer func() {
		if err := rec.Close(); err != nil {
			fmt.Fprintf(logf, "kyo: recording events failed: %v\n", err)
		}
	}()
	view := startView(opt, agg, gate, start, check)
	defer view.close() // restore the cursor even on a panic; idempotent

	res, err := runner.Run(ctx, runner.Options{
		Command: getenv(opt.Env, "KYO_ANSIBLE_PLAYBOOK"),
		Args:    opt.Args,
		Dir:     opt.Dir,
		Env:     env,
		RawLog:  logf,
		OnEvent: onEvent,
		OnStdout: func(line string) {
			sw.Stdout(line)
			gate.line(line)
		},
		OnStdoutPartial: gate.partial,
		OnStderr:        in.Stderr,
	})
	gate.stop()
	view.stop()
	if err != nil {
		view.clear()
		return 1, err
	}
	src := sw.Finish()
	end := opt.Now()
	agg.Finish(end)
	late := sw.Dropped()
	rec.End(recording.End{
		Time: end, ExitCode: res.ExitCode, Signaled: res.Signaled,
		Source: sourceNames[src], NoPlugin: noPlugin, Late: late,
	})
	if late > 0 {
		fmt.Fprintf(logf, "kyo: the callback plugin answered after the text was chosen; %d events ignored\n", late)
	}
	snap, elapsed := agg.Snapshot(), end.Sub(start)
	view.end(snap, elapsed)
	return report(opt, ending{
		snap: snap, elapsed: elapsed, logPath: logPath, check: check,
		src: src, noPlugin: noPlugin, late: late,
		exitCode: res.ExitCode, exitKnown: true,
	}), nil
}

// ending is what the summary needs from a finished run, live or replayed.
type ending struct {
	snap      aggregator.Snapshot
	elapsed   time.Duration
	logPath   string
	check     bool
	src       source.Source
	noPlugin  bool
	late      int
	exitCode  int  // the child's, as the runner reported it
	exitKnown bool // false for a recording that stops before the run ended
}

// report prints the summary, or why there is none, after the view has ended,
// and returns kyo's exit code for the run.
func report(opt Options, e ending) int {
	if e.src == source.None {
		why := "no events from the callback plugin"
		if e.noPlugin {
			why = "KYO_NO_PLUGIN is set"
		}
		fmt.Fprintf(opt.Stderr, "kyo: %s, and the output is not the default callback's; the run's output is in %s\n",
			why, e.logPath)
		return e.exitCode // nothing is known about changes, so no detailed code
	}
	width, height := render.Size(opt.Stderr)
	if height == 0 {
		width = 0 // not a terminal: lines stay whole
	}
	code := e.exitCode
	var note string
	if e.exitKnown {
		code = exitCode(e.exitCode, e.snap, getenv(opt.Env, "KYO_DETAILED_EXIT") == "1", e.check)
	}
	if code != e.exitCode {
		note = fmt.Sprintf("exit %d: changes pending (KYO_DETAILED_EXIT)", code)
	}
	render.Summary(opt.Stderr, e.snap, render.SummaryOpts{
		LogPath:   e.logPath,
		Elapsed:   e.elapsed,
		Width:     width,
		ArgsCheck: e.check,
		Styles:    render.NewStyles(opt.Stderr, opt.Env),
		Note:      note,
	})
	switch e.src {
	case source.Text:
		why := "the callback plugin sent no events"
		if e.late > 0 {
			why = "the callback plugin answered too late"
		}
		fmt.Fprintf(opt.Stderr, "  read from the default callback's output: %s (no task totals or diff sizes)\n", why)
	case source.TextForce:
		fmt.Fprintln(
			opt.Stderr,
			"  read from the default callback's output: KYO_NO_PLUGIN is set (no task totals or diff sizes)",
		)
	case source.None, source.Plugin:
	}
	return code
}

// exitCode is kyo's exit code for a run some source fed: the child's own,
// which includes 128+signal for a signal death, except that with detailed set
// a plan that succeeded with changes pending exits ExitChangesPending. A
// failed plan keeps its code, because a failure is not drift. An apply
// ignores detailed.
func exitCode(child int, snap aggregator.Snapshot, detailed, argsCheck bool) int {
	if detailed && child == 0 && render.IsPlan(snap, argsCheck) && len(snap.Changes) > 0 {
		return ExitChangesPending
	}
	return child
}

// inputs is the aggregator's front door: each event and stderr line is
// recorded and fed under one lock, so the recording holds them in the order
// the aggregator saw them. The source's grace timer feeds from its own
// goroutine, outside the runner's lock, so the runner's lock is not enough.
type inputs struct {
	mu  sync.Mutex
	agg *aggregator.Aggregator
	rec *recording.Writer
	now func() time.Time
}

func (in *inputs) Event(ev events.Event) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.rec.Event(ev)
	in.agg.Feed(ev)
}

func (in *inputs) Stderr(line string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.rec.Stderr(in.now(), line)
	in.agg.Stderr(line)
}

// sourceNames are the sources as a recording names them.
var sourceNames = map[source.Source]string{
	source.None:      "none",
	source.Plugin:    "plugin",
	source.Text:      "text",
	source.TextForce: "text-force",
}

// recordingPath is where a run's events go, next to its raw log:
// kyo-<ts>.log records to kyo-<ts>.events.jsonl. A discarded log records
// nothing.
func recordingPath(logPath string) string {
	if logPath == os.DevNull {
		return ""
	}
	return strings.TrimSuffix(logPath, ".log") + ".events.jsonl"
}

// startRecording opens the recording for logPath, or returns nil (record
// nothing) with the reason in the raw log.
func startRecording(logPath string, logf io.Writer) *recording.Writer {
	path := recordingPath(logPath)
	if path == "" {
		return nil
	}
	rec, err := recording.Create(path)
	if err != nil {
		fmt.Fprintf(logf, "kyo: not recording events: %v\n", err)
		return nil
	}
	return rec
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

func startView(opt Options, agg *aggregator.Aggregator, gate *promptGate, start time.Time, check bool) *view {
	v := &view{w: opt.Stderr, done: make(chan struct{})}
	styles := render.NewStyles(opt.Stderr, opt.Env)

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
					snap := agg.Snapshot()
					gate.paint(func() { v.plain.Update(snap, opt.Now().Sub(start)) })
				}
			}
		})
		return v
	}

	v.live = render.NewLive(opt.Stderr)
	gate.onOpen = func() {
		v.live.Clear()
		v.live.Close() // the cursor shows for typing; the next paint hides it
	}
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
				frame := render.Frame(snap, fo)
				gate.paint(func() { v.live.Render(frame) })
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

// valueFlags are ansible-playbook options that take the next argument as
// their value, so a .yml after them is not the playbook.
var valueFlags = []string{
	"-i", "--inventory", "--inventory-file", "-e", "--extra-vars", "-l", "--limit",
	"-t", "--tags", "--skip-tags", "-u", "--user", "-c", "--connection", "-f", "--forks",
	"-M", "--module-path", "-T", "--timeout", "--vault-id", "--vault-password-file",
	"--vault-pass-file", "--private-key", "--key-file", "--become-method", "--become-user",
	"--become-password-file", "--connection-password-file", "--start-at-task",
	"--ssh-common-args", "--sftp-extra-args", "--scp-extra-args", "--ssh-extra-args",
}

// playbookFromArgs reads what the plugin's playbook event would carry from the
// command line, for the text source: the first playbook's file name, --check
// and --diff.
func playbookFromArgs(args []string) textparse.Playbook {
	pb := textparse.Playbook{Check: argsCheck(args)}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--diff" || a == "-D":
			pb.Diff = true
		case slices.Contains(valueFlags, a):
			i++
		case pb.File == "" && !strings.HasPrefix(a, "-") &&
			(strings.HasSuffix(a, ".yml") || strings.HasSuffix(a, ".yaml")):
			pb.File = filepath.Base(a)
		}
	}
	return pb
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
