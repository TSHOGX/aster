package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"harness-platform/orchestrator/internal/agentrunner"
)

func main() {
	var c agentrunner.Config
	flag.StringVar(&c.Session, "session", "", "Agent Session identity")
	flag.StringVar(&c.Incarnation, "incarnation", "", "process incarnation identity")
	flag.StringVar(&c.StateRoot, "state-root", "/runtime", "Project retained runtime root")
	flag.StringVar(&c.Workspace, "workspace", "/workspace", "Project shared workspace")
	flag.StringVar(&c.PiBinary, "pi", "/usr/local/bin/pi", "native Pi executable")
	flag.Int64Var(&c.JournalBytes, "journal-bytes", 64<<20, "durable journal byte budget")
	worker := flag.Bool("worker", false, "internal supervised worker")
	replay := flag.Bool("replay", false, "replay original journal without starting Pi")
	owner := flag.Uint64("owner", 0, "journal recovery owner")
	after := flag.Uint64("after", 0, "committed ingest position")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if *replay {
		if err := agentrunner.Replay(ctx, c, *owner, *after, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(12)
		}
		return
	}
	if !*worker {
		binary, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(agentrunner.Guard(ctx, binary, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	// The service credential arrives in the start environment rather than as a
	// flag, so it never appears in the guest process table. It is cleared here so
	// Pi and its tool descendants do not inherit it as an ambient variable.
	c.ServiceCredential = os.Getenv("PROJECT_SERVICE_CREDENTIAL")
	if err := os.Unsetenv("PROJECT_SERVICE_CREDENTIAL"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := agentrunner.Run(ctx, c, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
