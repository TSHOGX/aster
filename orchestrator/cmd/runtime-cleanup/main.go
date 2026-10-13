package main

import (
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/projectfiles"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := flag.String("root", "", "saved Project runtime root")
	flag.Parse()
	if !filepath.IsAbs(*root) || filepath.Clean(*root) != *root || *root == "/" || flag.NArg() != 1 || strings.ContainsAny(flag.Arg(0), "/\\") || projectfiles.Path(flag.Arg(0), false) != nil {
		fmt.Fprintln(os.Stderr, "canonical runtime --root and one Session identity required")
		os.Exit(2)
	}
	if err := projectfiles.RemoveTree(*root, ".history-"+flag.Arg(0)+".jsonl"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := projectfiles.RemoveTree(filepath.Join(*root, "sessions"), flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
