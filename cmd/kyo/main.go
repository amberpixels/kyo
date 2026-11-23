// Command kyo wraps ansible-playbook: the same arguments, a readable result.
//
//	kyo <ansible-playbook args>   run the playbook; --check makes it a plan
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/amberpixels/kyo/internal/engine"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kyo <ansible-playbook args>   (--check makes it a plan)")
		os.Exit(2)
	}
	if args[0] == "-V" || (len(args) == 1 && args[0] == "--version") {
		fmt.Println("kyo", version)
		os.Exit(0)
	}
	code, err := engine.Run(context.Background(), engine.Options{Args: args})
	if err != nil {
		fmt.Fprintln(os.Stderr, "kyo:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
