// Package runner spawns ansible-playbook with kyo's event side channel: an
// inherited pipe on fd 3 that the callback plugin writes JSON lines to. It tees
// the child's stdout and stderr into the raw log, hands decoded events and
// stderr lines to handlers, and forwards SIGINT/SIGTERM.
package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// eventFD is the child's fd number for the side channel: ExtraFiles[0] lands on
// fd 3 (after stdin, stdout, stderr).
const eventFD = 3

// drainGrace bounds how long Run waits for the side channel after the child has
// exited. The plugin marks the fd non-inheritable so ssh children can't hold it
// open, but EOF is still not something to block on forever.
const drainGrace = 2 * time.Second

// Options configures a run.
type Options struct {
	Command string   // binary; default "ansible-playbook"
	Args    []string // user's argv, passed untouched
	Dir     string   // working dir; empty means the current one
	Env     []string // full child env; nil means os.Environ()

	Stdin  io.Reader // default os.Stdin, so prompts keep working
	RawLog io.Writer // every stdout and stderr line, as printed

	// Handlers are called from the runner's reader goroutines, one at a time
	// (the runner serializes them), in arrival order per stream.
	OnEvent  func(events.Event)
	OnStderr func(line string)
}

// Result reports how the child exited and what the side channel delivered.
type Result struct {
	ExitCode  int  // exit code, or 128+signal
	Signaled  bool // killed by a signal
	Events    int  // events decoded
	BadEvents int  // side-channel lines that failed to decode
}

// Run launches the playbook and waits for it and its streams to finish. A
// nonzero child exit is reported in Result, not as an error; errors are for
// failures to start or read.
func Run(ctx context.Context, opt Options) (*Result, error) {
	command := opt.Command
	if command == "" {
		command = "ansible-playbook"
	}
	env := opt.Env
	if env == nil {
		env = os.Environ()
	}

	evR, evW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating event pipe: %w", err)
	}
	defer evR.Close()
	// The write end goes to the child; ours must be closed once it has started,
	// or on any early return.
	closeW := sync.OnceFunc(func() { _ = evW.Close() })
	defer closeW()

	cmd := exec.CommandContext(ctx, command, opt.Args...)
	cmd.Dir = opt.Dir
	cmd.Env = append(slices.Clone(env), fmt.Sprintf("KYO_FD=%d", eventFD))
	cmd.Stdin = opt.Stdin
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.ExtraFiles = []*os.File{evW}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	closeW() // the child holds its own copy; ours would keep the pipe open

	stop := forwardSignals(cmd)
	defer stop()

	var (
		mu  sync.Mutex // serializes log writes and handler calls
		res Result
		wg  sync.WaitGroup
	)
	logLine := func(line string) {
		if opt.RawLog != nil {
			_, _ = io.WriteString(opt.RawLog, line+"\n")
		}
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, func(line string) {
			mu.Lock()
			logLine(line)
			mu.Unlock()
		})
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, func(line string) {
			mu.Lock()
			logLine(line)
			if opt.OnStderr != nil {
				opt.OnStderr(line)
			}
			mu.Unlock()
		})
	}()

	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		scanLines(evR, func(line string) {
			ev, err := events.Decode([]byte(line))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.BadEvents++
				return
			}
			res.Events++
			if opt.OnEvent != nil {
				opt.OnEvent(ev)
			}
		})
	}()

	// stdout/stderr must be fully read before Wait, which closes them.
	wg.Wait()
	waitErr := cmd.Wait()

	select {
	case <-evDone:
	case <-time.After(drainGrace):
		_ = evR.Close() // unblocks the reader; whatever was buffered is already in
		<-evDone
	}

	code, signaled := exitStatus(waitErr)
	mu.Lock()
	res.ExitCode, res.Signaled = code, signaled
	out := res
	mu.Unlock()

	if waitErr != nil && code < 0 {
		return &out, waitErr
	}
	return &out, nil
}

// scanLines feeds each line of r to fn. Lines up to 4 MB are kept whole (debug
// dumps and big diffs get long).
func scanLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
}

// forwardSignals relays SIGINT/SIGTERM to the child and returns a stop func.
func forwardSignals(cmd *exec.Cmd) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-ch:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// exitStatus maps cmd.Wait's error to an exit code (128+signal for a signal
// death). It returns -1 for an error that isn't an exit status at all.
func exitStatus(waitErr error) (int, bool) {
	if waitErr == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		return -1, false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), true
	}
	return ee.ExitCode(), false
}
