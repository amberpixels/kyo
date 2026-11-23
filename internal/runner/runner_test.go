package runner

import (
	"context"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestExitCodePassesThrough(t *testing.T) {
	res, err := Run(context.Background(), Options{Command: "sh", Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.Signaled {
		t.Errorf("got code %d signaled %v, want 3 false", res.ExitCode, res.Signaled)
	}
}

func TestSignalDeathIs128PlusSignal(t *testing.T) {
	res, err := Run(context.Background(), Options{Command: "sh", Args: []string{"-c", "kill -TERM $$"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 128+int(syscall.SIGTERM) || !res.Signaled {
		t.Errorf("got code %d signaled %v, want 143 true", res.ExitCode, res.Signaled)
	}
}

func TestShouldForward(t *testing.T) {
	fg := func() bool { return true }
	bg := func() bool { return false }
	cases := []struct {
		sig  syscall.Signal
		fg   func() bool
		want bool
	}{
		{syscall.SIGINT, fg, false}, // the terminal already sent it to the child
		{syscall.SIGINT, bg, true},  // from kill, or no terminal
		{syscall.SIGTERM, fg, true},
		{syscall.SIGTERM, bg, true},
	}
	for _, c := range cases {
		if got := shouldForward(c.sig, c.fg); got != c.want {
			t.Errorf("shouldForward(%v, fg=%v) = %v, want %v", c.sig, c.fg(), got, c.want)
		}
	}
}

// chunkReader returns one chunk per Read, so a test controls where reads end.
type chunkReader struct{ chunks []string }

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

func TestReadLinesReportsTheTail(t *testing.T) {
	var lines, tails []string
	r := &chunkReader{chunks: []string{"a\nprompt: ", "x\r\nb\n", "end"}}
	readLines(r, func(l string) { lines = append(lines, l) }, func(tl string) { tails = append(tails, tl) })

	if want := []string{"a", "prompt: x", "b", "end"}; !slices.Equal(lines, want) {
		t.Errorf("lines %q, want %q", lines, want)
	}
	if want := []string{"prompt: ", "", "end", ""}; !slices.Equal(tails, want) {
		t.Errorf("tails %q, want %q", tails, want)
	}
}

func TestReadLinesLongLineKeepsReading(t *testing.T) {
	long := strings.Repeat("x", maxLine+10)
	var lines []string
	readLines(strings.NewReader(long+"\nnext\n"), func(l string) { lines = append(lines, l) }, func(string) {})

	if len(lines) < 2 || lines[len(lines)-1] != "next" {
		t.Fatalf("got %d lines, last %q", len(lines), lines[len(lines)-1])
	}
	if got := len(strings.Join(lines[:len(lines)-1], "")); got != len(long) {
		t.Errorf("long line pieces hold %d bytes, want %d", got, len(long))
	}
}
