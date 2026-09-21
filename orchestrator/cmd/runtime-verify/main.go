package main

import (
	"fmt"
	"harness-platform/orchestrator/internal/runtimerelease"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "expected runtime digest required")
		os.Exit(2)
	}
	if err := runtimerelease.Verify("/", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
