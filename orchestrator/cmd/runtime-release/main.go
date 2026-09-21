package main

import (
	"flag"
	"fmt"
	"harness-platform/orchestrator/internal/runtimerelease"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := flag.String("root", "", "newly assembled template root")
	output := flag.String("output", "", "exported manifest path")
	flag.Parse()
	if *root == "" || *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--root and --output required")
		os.Exit(2)
	}
	manifest, err := runtimerelease.Generate(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	raw, err := runtimerelease.Bytes(manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	digest, err := runtimerelease.Digest(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, name := range []string{filepath.Join(*root, strings.TrimPrefix(runtimerelease.ManifestPath, "/")), *output} {
		if err = os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		file, e := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		_, e = file.Write(append(raw, '\n'))
		if e == nil {
			e = file.Sync()
		}
		closeErr := file.Close()
		if e != nil || closeErr != nil {
			fmt.Fprintln(os.Stderr, e, closeErr)
			os.Exit(1)
		}
	}
	if err = os.WriteFile(*output+".sha256", []byte(digest+"\n"), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(digest)
}
