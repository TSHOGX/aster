package main

import (
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/runtimerelease"
	"os"
)

func main() {
	root := flag.String("root", "/", "runtime filesystem to verify (read-only)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "expected runtime digest required")
		os.Exit(2)
	}
	if err := runtimerelease.Verify(*root, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
