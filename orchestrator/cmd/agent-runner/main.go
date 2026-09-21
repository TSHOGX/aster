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
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := agentrunner.Run(ctx, c, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
