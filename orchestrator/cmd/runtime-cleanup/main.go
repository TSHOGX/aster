package main

import (
	"fmt"
	"harness-platform/orchestrator/internal/projectfiles"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 || strings.ContainsAny(os.Args[1], "/\\") || projectfiles.Path(os.Args[1], false) != nil {
		fmt.Fprintln(os.Stderr, "one Session identity required")
		os.Exit(2)
	}
	if err := projectfiles.RemoveTree("/runtime/sessions", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
