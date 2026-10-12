package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/projectfiles"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", "", "saved Project workspace root")
	reader := flag.Bool("reader", false, "serve read-only workspace protocol")
	flag.Parse()
	if !filepath.IsAbs(*root) || filepath.Clean(*root) != *root || *root == "/" {
		fmt.Fprintln(os.Stderr, "canonical workspace --root required")
		os.Exit(2)
	}
	if *reader && flag.NArg() == 0 {
		if err := projectfiles.ServeReader(context.Background(), *root, os.Stdin, os.Stdout, 0); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	var request projectfiles.Request
	if *reader || flag.NArg() != 1 || json.Unmarshal([]byte(flag.Arg(0)), &request) != nil {
		fmt.Fprintln(os.Stderr, "one operation document required")
		os.Exit(2)
	}
	if err := projectfiles.Execute(*root, request, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, projectfiles.ErrUncertain) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}
