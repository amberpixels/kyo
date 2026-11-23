// Command kyo wraps ansible-playbook: the same arguments, a readable result.
//
//	kyo <ansible-playbook args>   run the playbook; --check makes it a plan
//	kyo --replay <events.jsonl>   replay a recorded run through the view
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/amberpixels/kyo/internal/engine"
)

const usage = `usage: kyo <ansible-playbook args>   (--check makes it a plan)
       kyo --replay <events.jsonl>`

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if args[0] == "-V" || (len(args) == 1 && args[0] == "--version") {
		fmt.Println("kyo", version)
		os.Exit(0)
	}
	var (
		code int
		err  error
	)
	if path, ok, replay := replayArg(args); replay {
		if !ok {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		code, err = engine.Replay(context.Background(), path, engine.Options{})
	} else {
		code, err = engine.Run(context.Background(), engine.Options{Args: args})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kyo:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// replayArg reads `--replay <path>` or `--replay=<path>` as the whole command
// line. It returns the path, whether the rest of the line was just that path,
// and whether --replay came first at all.
func replayArg(args []string) (string, bool, bool) {
	if p, found := strings.CutPrefix(args[0], "--replay="); found {
		return p, len(args) == 1 && p != "", true
	}
	if args[0] != "--replay" {
		return "", false, false
	}
	if len(args) != 2 || args[1] == "" {
		return "", false, true
	}
	return args[1], true, true
}
