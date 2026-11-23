// Package render turns an aggregator.Snapshot into human output: a block
// repainted in place on a terminal, one-line milestones everywhere else, and
// the plan or apply summary printed once at the end. Color comes from
// lipgloss Styles that degrade to plain text off a terminal and under NO_COLOR.
package render

import (
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"
)

// plainEnv are the variables that ask for plain output. NO_COLOR is not one:
// it removes color, and a repainted block is not color.
var plainEnv = []string{"KYO_PLAIN", "CI", "CLAUDECODE"}

// Plain reports whether output to w should be milestone lines instead of a
// repainted block: w is not a terminal, or env asks for it.
func Plain(w io.Writer, env []string) bool {
	for _, k := range plainEnv {
		if truthy(Getenv(env, k)) {
			return true
		}
	}
	f, ok := w.(*os.File)
	return !ok || !term.IsTerminal(int(f.Fd()))
}

// defaultWidth is the assumed width when w can't be measured.
const defaultWidth = 100

// Size returns the terminal width and height behind w. Off a terminal, width
// falls back to defaultWidth and height is 0, meaning unknown: callers must not
// cap anything to it.
func Size(w io.Writer) (int, int) {
	if f, ok := w.(*os.File); ok {
		if cols, rows, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
			return cols, rows
		}
	}
	return defaultWidth, 0
}

// Getenv reads key from env, the last value winning as exec does.
func Getenv(env []string, key string) string {
	for _, e := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

// truthy reads a flag variable: set and not "0" or "false". CI=false is a
// developer turning CI detection off, not on.
func truthy(v string) bool {
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}
