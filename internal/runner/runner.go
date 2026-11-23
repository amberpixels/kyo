// Package runner spawns ansible-playbook with kyo's event side channel: an
// inherited pipe on fd 3 that the callback plugin writes JSON lines to. It tees
// the child's stdout and stderr into the raw log, hands decoded events, stdout
// and stderr lines to handlers, and forwards SIGTERM, and SIGINT when the
// terminal did not already deliver it.
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
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

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
	// (the runner serializes them), in arrival order per stream. A nil OnEvent
	// means no side channel at all: no fd 3 and no KYO_FD for the child.
	OnEvent  func(events.Event)
	OnStdout func(line string)
	OnStderr func(line string)

	// OnStdoutPartial gets stdout's unterminated tail after every read that
	// changes it, and "" once a newline completes it. A tail that stands is
	// usually a prompt: input() writes its text without a newline.
	OnStdoutPartial func(tail string)
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

	// The child stays in kyo's process group, the terminal's foreground one:
	// getpass and ansible's prompt_until refuse to prompt from any other.
	cmd := exec.CommandContext(ctx, command, opt.Args...)
	cmd.Dir = opt.Dir
	cmd.Env = slices.Clone(env)
	cmd.Stdin = opt.Stdin
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}

	var evR *os.File
	closeW := func() {}
	if opt.OnEvent != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("creating event pipe: %w", err)
		}
		defer r.Close()
		// The write end goes to the child; ours must be closed once it has
		// started, or on any early return.
		closeW = sync.OnceFunc(func() { _ = w.Close() })
		defer closeW()
		evR = r
		cmd.Env = append(cmd.Env, fmt.Sprintf("KYO_FD=%d", eventFD))
		cmd.ExtraFiles = []*os.File{w}
	}

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

	stop := forwardSignals(cmd, ttyForeground)
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
		readLines(stdout, func(line string) {
			mu.Lock()
			logLine(line)
			if opt.OnStdout != nil {
				opt.OnStdout(line)
			}
			mu.Unlock()
		}, func(tail string) {
			if opt.OnStdoutPartial == nil {
				return
			}
			mu.Lock()
			opt.OnStdoutPartial(tail)
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
	if evR == nil {
		close(evDone)
	} else {
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
				opt.OnEvent(ev)
			})
		}()
	}

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

// maxLine is the longest line kept whole (debug dumps and big diffs get long).
const maxLine = 4 * 1024 * 1024

// scanLines feeds each line of r to fn.
func scanLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		fn(sc.Text())
	}
}

// readLines feeds each complete line of r to fn, like scanLines, and reports
// the unterminated tail to partial whenever it changes: a non-empty tail after
// a read that left one, "" when a newline completes it. A line past maxLine is
// handed over in pieces rather than stopping the read, so the pipe keeps
// draining and the child never blocks on it.
func readLines(r io.Reader, fn, partial func(string)) {
	buf := make([]byte, 64*1024)
	var line []byte
	reported := false
	for {
		n, err := r.Read(buf)
		chunk := buf[:n]
		for len(chunk) > 0 {
			i := slices.Index(chunk, '\n')
			if i < 0 {
				line = append(line, chunk...)
				break
			}
			line = append(line, chunk[:i]...)
			chunk = chunk[i+1:]
			fn(strings.TrimSuffix(string(line), "\r"))
			line = line[:0]
			if reported {
				partial("")
				reported = false
			}
		}
		if len(line) > maxLine {
			fn(string(line))
			line = line[:0]
		}
		if len(line) > 0 && n > 0 {
			partial(string(line))
			reported = true
		}
		if err != nil {
			if len(line) > 0 {
				fn(string(line))
			}
			if reported {
				partial("")
			}
			return
		}
	}
}

// forwardSignals relays SIGINT/SIGTERM to the child and returns a stop func.
// A SIGINT is skipped while kyo owns the terminal (foreground reports true):
// the terminal sent it to the whole process group, the child included, and a
// second one makes ansible-playbook die of the signal (exit 130) instead of
// stopping cleanly with 99. Checked on ansible-core 2.22. A kill -INT aimed at
// kyo alone while it is in the foreground is therefore not forwarded.
func forwardSignals(cmd *exec.Cmd, foreground func() bool) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-ch:
				if cmd.Process != nil && shouldForward(s, foreground) {
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

// shouldForward reports whether s must be relayed to the child.
func shouldForward(s os.Signal, foreground func() bool) bool {
	return s != syscall.SIGINT || !foreground()
}

// ttyForeground reports whether kyo's process group is the foreground group of
// its controlling terminal, read from the first standard stream that is one.
// Without a terminal it is false: any SIGINT then came from kill.
func ttyForeground() bool {
	for _, f := range []*os.File{os.Stdin, os.Stderr, os.Stdout} {
		pgrp, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
		if err != nil {
			continue // not a terminal
		}
		return pgrp == unix.Getpgrp()
	}
	return false
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
