package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/nativecapture"
	capturesource "harness-platform/orchestrator/internal/nativecapture/guest"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/trajectory"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("native capture stopped", "error", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("native-capture", flag.ContinueOnError)
	control := flags.String("control-db", "", "existing managed control database")
	root := flags.String("trajectory-root", "", "immutable native archive root")
	endpoint := flags.String("engine-endpoint", "http://127.0.0.1:8090", "trusted Khora API endpoint")
	once := flags.Bool("once", false, "process one batch and exit")
	interval := flags.Duration("interval", 5*time.Second, "poll interval")
	timeout := flags.Duration("batch-timeout", 2*time.Minute, "maximum duration of one capture batch")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*control) || !filepath.IsAbs(*root) || *interval <= 0 || *timeout <= 0 {
		return fmt.Errorf("absolute control-db, trajectory-root and positive timing values required")
	}
	// Canonicalize the actual database so two symlink spellings cannot select
	// different publisher locks. This side process never runs schema migration.
	canonical, err := filepath.EvalSymlinks(*control)
	if err != nil {
		return err
	}
	release, err := schemastate.LockPaths(canonical + ".native-capture")
	if err != nil {
		return err
	}
	defer release()
	st, err := store.OpenForMaintenance(ctx, *control)
	if err != nil {
		return err
	}
	defer st.Close()
	backend, err := khora.New(ctx, khora.Config{Endpoint: *endpoint})
	if err != nil {
		return err
	}
	worker := nativecapture.Worker{Repository: st, Source: capturesource.Source{Backend: backend}, Sink: trajectory.NewCapturer(*root)}
	for {
		batch, cancel := context.WithTimeout(ctx, *timeout)
		count, err := worker.Step(batch, 32)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if *once {
			return err
		}
		if err != nil {
			slog.Warn("native capture batch incomplete", "completed", count, "error", err)
		} else if count != 0 {
			slog.Info("native captures committed", "completed", count)
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
