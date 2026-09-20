package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/retainedapp"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
)

// version is injected at build time via -ldflags, e.g.:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" ./cmd/orchestrator
var version = "dev"
var commit = "unknown"

func main() {
	if err := globalOptions(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "help") {
		fmt.Println("orchestrator [--config PATH] [serve|version|user|diagnostics]")
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("{\"version\":%q,\"commit\":%q}\n", version, commit)
		return
	}
	logLevel := new(slog.LevelVar)
	if os.Getenv("HARNESS_LOG_LEVEL") == "debug" {
		logLevel.Set(slog.LevelDebug)
	} else {
		logLevel.Set(slog.LevelInfo)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Subcommands run a bounded one-shot job (no server, no goroutines) then
	// exit; `user` also keeps stdout clean for CLI consumption, so the startup
	// banner below is only logged on the full server boot path.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "diagnostics":
			if err := runDiagnostics(ctx, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		case "user":
			if err := runUser(ctx, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		case "serve":
			if len(os.Args) != 2 {
				fmt.Fprintln(os.Stderr, "unexpected serve arguments")
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
			os.Exit(1)
		}
	}

	log.Info("starting orchestrator", "version", version, "commit", commit)

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	deploymentPath := os.Getenv("HARNESS_KHORA_DEPLOYMENT")
	if deploymentPath == "" {
		log.Error("HARNESS_KHORA_DEPLOYMENT is required")
		os.Exit(1)
	}
	khoraDeployment, err := retainedapp.LoadDeployment(deploymentPath)
	if err != nil {
		log.Error("invalid Khora deployment", "error", err)
		os.Exit(1)
	}

	owner, err := store.AcquireOwnerLock(cfg.Harness.RunDir)
	if err != nil {
		log.Error("failed to acquire orchestrator owner lock", "error", err)
		os.Exit(1)
	}
	defer owner.Close()

	unlockDatabases, lockErr := schemastate.LockPaths(cfg.DBPath)
	if lockErr != nil {
		log.Error("database lock failed", "error", lockErr)
		os.Exit(1)
	}
	defer unlockDatabases()

	db, err := store.OpenWithOptions(ctx, cfg.DBPath, store.Options{})
	if err != nil {
		log.Error("failed to open store", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.WriteOwner(ctx, owner); err != nil {
		log.Error("failed to write orchestrator owner", "error", err)
		os.Exit(1)
	}
	if err := db.ReconcileOfflineWorkspaceReaders(ctx, store.GenerationLeaseOwner(owner.UUID)); err != nil {
		log.Error("failed to reconcile replaced workspace readers", "error", err)
		os.Exit(1)
	}
	retained, err := constructKhoraApplication(ctx, cfg, db, owner.UUID, khoraDeployment, log)
	if err != nil {
		log.Error("failed Khora startup", "error", err)
		os.Exit(1)
	}
	defer retained.Close()
	app := retained.app
	app.SetVersion(version)
	app.SetBuildCommit(commit)
	// Snapshots for public share links. A failure here is fatal rather
	// than degrading to text-only links: the alternative is a deployment
	// that silently rejects every file share with a 503.
	shareBlobs, err := shareblob.New(cfg.ShareBlobRoot)
	if err != nil {
		log.Error("open share blob store", "error", err)
		os.Exit(1)
	}
	app.SetShareBlobStore(shareBlobs)
	ownerHeartbeatErr := db.StartOwnerHeartbeat(ctx, owner)
	if err := db.EnsureUser(ctx, "lab", "Lab User"); err != nil {
		log.Error("failed to ensure lab user", "error", err)
		os.Exit(1)
	}

	go func() {
		if err := <-ownerHeartbeatErr; err != nil {
			log.Error("orchestrator owner heartbeat failed", "error", err)
			stop()
		}
	}()

	retainedDone := make(chan error, 1)
	go func() {
		err := retained.Run(ctx)
		retainedDone <- err
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("Khora services stopped", "error", err)
			stop()
		}
	}()

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: app.Routes(),
		// ReadHeaderTimeout and ReadTimeout bound how long a client may take to
		// send headers/body, defending against Slowloris-style slow-send DoS.
		// IdleTimeout caps keep-alive idle connections. WriteTimeout is left
		// unset on purpose: the /api/events/stream SSE endpoint holds a single
		// response open for the life of the session and any write deadline would
		// sever it. nginx's proxy_read_timeout (3600s) bounds those streams.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB, well above any legitimate header set
	}

	go func() {
		log.Info("orchestrator listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	// net/http.Server.Shutdown does not cancel active request contexts. Signal
	// the application first so SSE/WebSocket handlers leave their long-lived
	// loops; otherwise graceful shutdown waits until its deadline for clients to
	// disconnect on their own.
	app.BeginShutdown()
	if retained == nil {
	}

	// Both servers shut down concurrently under one overall budget. Using one
	// context sequentially made the second Shutdown inherit whatever time the
	// first had consumed and report a misleading timeout. If a handler still
	// refuses to return by the deadline, Close is the explicit hard-stop
	// fallback; the process is already exiting and lingering connections must not
	// hold the old PID/port hostage.
	shutdownDeadline := time.Now().Add(10 * time.Second)
	shutdown := func(name string, srv *http.Server) {
		if srv == nil {
			return
		}
		remaining := time.Until(shutdownDeadline)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), remaining)
		err := srv.Shutdown(shutdownCtx)
		cancel()
		if err == nil {
			return
		}
		log.Warn(name+" graceful shutdown failed; forcing connections closed", "error", err)
		if closeErr := srv.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			log.Warn(name+" forced shutdown failed", "error", closeErr)
		}
	}
	var shutdownWG sync.WaitGroup
	shutdownWG.Add(1)
	go func() {
		defer shutdownWG.Done()
		shutdown("http", httpServer)
	}()
	shutdownWG.Wait()
	if retainedDone != nil {
		retained.Close()
		select {
		case <-retainedDone:
		case <-time.After(10 * time.Second):
			log.Error("retained services did not finish shutdown")
		}
	}
}
