package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/artifacts"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/controlsource"
	"harness-platform/orchestrator/internal/deploymentgate"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/memory"
	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/retainedapp"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/trajectory"
	localworkspace "harness-platform/orchestrator/internal/workspace/local"
	"net"
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
		fmt.Println("orchestrator [--config PATH] [serve|version|db status|db plan|db migrate|user|kb|backfill|replay]")
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
		if (os.Args[1] == "backfill" || os.Args[1] == "replay") && deploymentgate.Paused() {
			fmt.Fprintln(os.Stderr, "deployment is in progress; standalone writers are paused")
			os.Exit(1)
		}
		switch os.Args[1] {
		case "db":
			if err := runDB(ctx, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "db-split":
			if err := runDBSplit(ctx, log, os.Args[2:]); err != nil {
				log.Error("db-split failed", "error", err)
				os.Exit(1)
			}
			return
		case "backfill":
			if err := runBackfill(ctx, log, os.Args[2:]); err != nil {
				log.Error("backfill failed", "error", err)
				os.Exit(1)
			}
			return
		case "replay":
			if err := runReplay(ctx, log, os.Args[2:]); err != nil {
				log.Error("replay failed", "error", err)
				os.Exit(1)
			}
			return
		case "kb":
			if err := runKB(ctx, log, os.Args[2:]); err != nil {
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

	if os.Getenv("HARNESS_MANAGED") == "1" && (version == "dev" || len(commit) != 40) {
		log.Error("managed service requires a versioned release binary with commit metadata")
		os.Exit(1)
	}
	log.Info("starting orchestrator", "version", version, "commit", commit)

	cfg, err := config.Load()
	if err != nil {
		log.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	var khoraDeployment *retainedapp.Deployment
	if path := os.Getenv("HARNESS_KHORA_DEPLOYMENT"); path != "" {
		khoraDeployment, err = retainedapp.LoadDeployment(path)
		if err != nil {
			log.Error("invalid Khora deployment", "error", err)
			os.Exit(1)
		}
	} else if _, err := config.ValidateIsolationRoots(cfg.IsolationRoots()); err != nil {
		log.Error("invalid isolation roots", "error", err)
		os.Exit(1)
	}
	for _, warning := range cfg.Warnings {
		log.Warn("config warning", "warning", warning)
	}

	owner, err := store.AcquireOwnerLock(cfg.Harness.RunDir)
	if err != nil {
		log.Error("failed to acquire orchestrator owner lock", "error", err)
		os.Exit(1)
	}
	defer owner.Close()

	unlockDatabases, lockErr := schemastate.LockPaths(cfg.DBPath, cfg.MemoryDBPath)
	if lockErr != nil {
		log.Error("database lock failed", "error", lockErr)
		os.Exit(1)
	}
	defer unlockDatabases()

	db, err := store.OpenWithOptions(ctx, cfg.DBPath, store.Options{AgentHomesRoot: cfg.AgentHomesRoot, Managed: os.Getenv("HARNESS_MANAGED") == "1"})
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
	// The memory/distillation database is physically separate from the control
	// plane, with its own writer connection, so a distillation sweep can no
	// longer take the lock that serves HTTP traffic and session state. It is
	// opened unconditionally (not only when the pipeline is enabled) so its
	// schema is present and the CLI paths find a migrated database.
	memoryDB, err := memorystore.OpenWithOptions(ctx, cfg.MemoryDBPath, memorystore.Options{Managed: os.Getenv("HARNESS_MANAGED") == "1"})
	if err != nil {
		log.Error("failed to open memory store", "error", err, "path", cfg.MemoryDBPath)
		os.Exit(1)
	}
	defer memoryDB.Close()
	// The ingest boundary: reads control, writes memory, never the reverse.
	memoryIngest := memorystore.NewIngestor(memoryDB, controlsource.New(db))
	var app *server.Server
	var watcher *artifacts.Watcher
	var retained *khoraApplication
	if khoraDeployment != nil {
		retained, err = constructKhoraApplication(ctx, cfg, db, owner.UUID, khoraDeployment, log)
		if err != nil {
			log.Error("failed Khora startup", "error", err)
			os.Exit(1)
		}
		defer retained.Close()
		app = retained.app
	} else {
		startup, err := runStartupRuntimeRecovery(ctx, cfg, owner.UUID, startupRuntimeRecoveryHooks{
			ClearActiveSessionExpiry: db.ClearActiveSessionExpiry,
			ConstructRuntime: func() error {
				rt := runtime.New(runtime.Config{
					RunscRoot:               cfg.RunscRoot,
					RunscNetwork:            cfg.RunscNetwork,
					RunscOverlay2:           cfg.RunscOverlay2,
					SessionsRoot:            cfg.SessionsRoot,
					AgentHomesRoot:          cfg.AgentHomesRoot,
					CheckpointsRoot:         cfg.CheckpointsRoot,
					BundleRoot:              cfg.BundleRoot,
					RootFSPath:              cfg.RootFSPath,
					ResolverPath:            os.Getenv("HARNESS_RESOLV_CONF"),
					AgentsKBRoot:            cfg.AgentsKBRoot,
					SandboxDataProxyAliases: sandboxDataProxyAliases(cfg.Harness),
					ObsGateway:              obsGatewayConfig(cfg.Harness),
					DefaultAgent:            cfg.DefaultAgent,
					SandboxUID:              cfg.Harness.SandboxIdentity.UID,
					SandboxGID:              cfg.Harness.SandboxIdentity.GID,
					SandboxSupplementalGIDs: append([]int(nil), cfg.Harness.SandboxIdentity.SupplementalGIDs...),
					RunDir:                  cfg.Harness.RunDir,
					PreStartProbeAttempts:   cfg.Harness.Probe.PreStartAttempts,
					PreStartProbeInterval:   cfg.Harness.Probe.PreStartInterval.Duration,
					ProbeHealthzStatuses:    cfg.Harness.Probe.AcceptStatus.GetHealthz,
					BridgeHeartbeat:         cfg.Harness.Bridge.HeartbeatInterval.Duration,
					BridgePollInterval:      cfg.Harness.Bridge.PollInterval.Duration,
					CommandTimeout:          cfg.Harness.Runtime.CommandTimeout.Duration,
					SandboxMemoryLimitBytes: cfg.Harness.Runtime.SandboxMemoryLimitBytes,
					SandboxPidsLimit:        cfg.Harness.Runtime.SandboxPidsLimit,
				})
				hub := events.NewHub()
				volumeConfig, err := dataVolumeProvisionerConfig(cfg)
				if err != nil {
					return err
				}
				watcher = artifacts.New(volumeConfig, db, hub, log)
				app = server.New(cfg, db, rt, watcher, hub, log)
				app.SetOwnerUUID(owner.UUID)
				// The runtime reports every `runsc run` exit here; see
				// server.HandleSandboxExit. Wired after New because the handler needs
				// the store and the event hub the server owns.
				rt.SetContainerExitHandler(app.HandleSandboxExit)
				return nil
			},
			RecoverExpiredRuntimeResources: func(ctx context.Context, now time.Time) (store.StartupRecoveryResult, error) {
				return app.RecoverExpiredRuntimeResources(ctx, now)
			},
			ReapResources: db.ReapResources,
			DestroyReclaimableGenerationResources: func(ctx context.Context, now time.Time) {
				app.DestroyReclaimableGenerationResources(ctx, now)
			},
		})
		if err != nil {
			log.Error("failed startup runtime recovery", "error", err)
			os.Exit(1)
		}
		if startup.ClearedActiveSessionExpiries > 0 {
			log.Info("cleared active session expiry", "sessions", startup.ClearedActiveSessionExpiries)
		}
	}
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
	// Per-entry KB review. Injected here (not in server.New) because the
	// KB writer and the memory DB are both optional: with no KB root
	// configured the review endpoints report 503 rather than the process
	// refusing to start.
	app.SetKBReview(memory.NewKB(cfg.AgentsKBRoot), memoryDB)
	ownerHeartbeatErr := db.StartOwnerHeartbeat(ctx, owner)
	if err := db.EnsureUser(ctx, "lab", "Lab User"); err != nil {
		log.Error("failed to ensure lab user", "error", err)
		os.Exit(1)
	}
	if retained == nil {
		if err := os.MkdirAll(cfg.AgentHomesRoot, 0o755); err != nil {
			log.Error("failed to create agent homes root", "error", err)
			os.Exit(1)
		}
	}
	if err := ensureAgentsKB(cfg.AgentsKBRoot, agentsKBSeedDir(cfg.RepoRoot)); err != nil {
		log.Error("failed to ensure agents knowledge base", "error", err)
		os.Exit(1)
	}

	go func() {
		if err := <-ownerHeartbeatErr; err != nil {
			log.Error("orchestrator owner heartbeat failed", "error", err)
			stop()
		}
	}()

	var retainedDone chan error
	if retained != nil {
		retainedDone = make(chan error, 1)
		go func() {
			err := retained.Run(ctx)
			retainedDone <- err
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Error("Khora services stopped", "error", err)
				stop()
			}
		}()
	} else {
		go func() {
			if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("artifact watcher stopped", "error", err)
			}
		}()

		// Start idle session monitoring
		go func() {
			if err := app.MonitorIdleSessions(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("idle session monitor stopped", "error", err)
			}
		}()
		go func() {
			if err := app.RunMaintenance(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("maintenance stopped", "error", err)
			}
		}()

	}

	// Nightly trajectory-distillation loop: normalize session trajectories,
	// extract memory/skills via the configured LLM, and publish promoted
	// knowledge into the shared /agents knowledge base. Disabled (nil) when no
	// memory LLM is configured.
	// The background nightly loop honors the `enabled` flag in memory-llm.yaml.
	// Setting enabled:false keeps the KB/agents mount live (seed carried forward)
	// while suppressing the automatic sweep — used to run cold-start/backfill
	// out-of-band (e.g. a scheduled overnight job) without the server's own timer
	// competing for the DB or firing an expensive re-harvest during the day.
	if !cfg.MemoryLLM.Enabled {
		log.Info("nightly distillation loop disabled (memory-llm enabled:false)")
	} else if pipeline := memory.NewPipeline(memory.PipelineOptions{
		Store:  memoryDB,
		Ingest: memoryIngest,
		Extractor: memory.NewExtractor(memory.Config{
			BaseURL: cfg.MemoryLLM.BaseURL,
			APIKey:  cfg.MemoryLLM.APIKey,
			Model:   cfg.MemoryLLM.Model,
		}),
		KB:       memory.NewKB(cfg.AgentsKBRoot),
		Log:      log,
		Capturer: trajectory.NewCapturer(memoryDB, memoryIngest, cfg.TrajectoryRoot, log),
		Agent: memory.NewAgentRunner(memory.AgentConfig{
			Bin:      cfg.MemoryLLM.AgentBin,
			Provider: cfg.MemoryLLM.AgentProvider,
			Model:    cfg.MemoryLLM.AgentModel,
			WorkDir:  cfg.RepoRoot, // Node C reads the live tree from here
		}),
		Agentic:     cfg.MemoryLLM.Agentic,
		AutoPublish: cfg.MemoryLLM.AutoPublish,
	}); pipeline != nil {
		log.Info("nightly distillation pipeline enabled",
			"agentic", cfg.MemoryLLM.Agentic, "auto_publish", cfg.MemoryLLM.AutoPublish)
		go func() {
			if err := pipeline.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("distillation pipeline stopped", "error", err)
			}
		}()
	}

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
	var proxyCorrelationServer *http.Server
	var proxyCorrelationListener net.Listener
	var proxyCorrelationSocket string
	if retained == nil {
		proxyCorrelationServer = app.ProxyCorrelationServer()
		proxyCorrelationListener, proxyCorrelationSocket, err = app.ListenProxyCorrelation()
		if err != nil {
			log.Error("failed to listen on proxy correlation socket", "error", err)
			os.Exit(1)
		}
	}

	go func() {
		log.Info("orchestrator listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "error", err)
			stop()
		}
	}()
	if retained == nil {
		go func() {
			log.Info("proxy correlation listening", "socket", proxyCorrelationSocket)
			if err := proxyCorrelationServer.Serve(proxyCorrelationListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("proxy correlation server stopped", "error", err)
				stop()
			}
		}()

	}

	<-ctx.Done()
	// net/http.Server.Shutdown does not cancel active request contexts. Signal
	// the application first so SSE/WebSocket handlers leave their long-lived
	// loops; otherwise graceful shutdown waits until its deadline for clients to
	// disconnect on their own.
	app.BeginShutdown()
	if retained == nil {
		_ = os.Remove(proxyCorrelationSocket)
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
	shutdownWG.Add(2)
	go func() {
		defer shutdownWG.Done()
		shutdown("proxy correlation", proxyCorrelationServer)
	}()
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

func dataVolumeProvisionerConfig(cfg config.Config) (localworkspace.DataVolumeProvisionerConfig, error) {
	roots, err := config.ValidateIsolationRoots(cfg.IsolationRoots())
	if err != nil {
		return localworkspace.DataVolumeProvisionerConfig{}, err
	}
	identity := cfg.Harness.SandboxIdentity
	return localworkspace.DataVolumeProvisionerConfig{
		SessionsRoot:   roots.SessionsRoot,
		AgentHomesRoot: roots.AgentHomesRoot,
		EvidenceRoot:   roots.DataVolumeEvidenceRoot,
		LayoutVersion:  localworkspace.DataVolumeLayoutVersion,
		RuntimeIdentity: store.RuntimeIdentity{
			UID:              identity.UID,
			GID:              identity.GID,
			SupplementalGIDs: identity.SupplementalGIDs,
		},
	}, nil
}

// sandboxDataProxyAliases collects the /etc/hosts aliases for enabled data
// proxies, in the sorted order EnabledDataProxies guarantees so the projection
// stays byte-stable. Returns nil when no data proxies are configured, which
// leaves the hosts file exactly as it was before harness.data_proxies existed.
func sandboxDataProxyAliases(h config.HarnessConfig) []string {
	proxies := h.EnabledDataProxies()
	if len(proxies) == 0 {
		return nil
	}
	out := make([]string, 0, len(proxies))
	for _, p := range proxies {
		if alias := p.Config.SandboxAlias; alias != "" {
			out = append(out, alias)
		}
	}
	return out
}

// obsGatewayConfig builds the OBS fake-credential settings from the configured
// data proxies. Disabled unless an enabled proxy declares kind obs_http, so a
// deployment without one writes no projection and keeps the control dir and its
// manifest digest byte-identical to before.
//
// The master key comes from the environment, never from harness.yaml, which is
// checked into git. It must match the gateway's own OBS_GATEWAY_MASTER_KEY or
// every signature the sandbox produces fails verification.
func obsGatewayConfig(h config.HarnessConfig) runtime.ObsGatewayConfig {
	for _, p := range h.EnabledDataProxies() {
		if p.Config.Upstream.Kind != config.DataProxyKindOBS {
			continue
		}
		region, bucket := obsUpstreamCoordinates(p.Config.Upstream.BaseURL)
		return runtime.ObsGatewayConfig{
			Enabled:   true,
			Alias:     p.Config.SandboxAlias,
			Port:      p.Config.SandboxPort,
			Region:    region,
			Bucket:    bucket,
			MasterKey: os.Getenv("OBS_GATEWAY_MASTER_KEY"),
		}
	}
	return runtime.ObsGatewayConfig{}
}

// obsUpstreamCoordinates extracts the region and bucket the skill needs.
//
// The region is the second dotted label of an OBS endpoint
// (obs.<region>.myhuaweicloud.com). The bucket is not part of the upstream URL —
// the gateway owns it — so it is read from the environment alongside the
// credentials, defaulting to the one bucket this gateway fronts.
func obsUpstreamCoordinates(baseURL string) (region, bucket string) {
	bucket = strings.TrimSpace(os.Getenv("OBS_BUCKET"))
	if bucket == "" {
		bucket = "vhr-parquets"
	}
	region = strings.TrimSpace(os.Getenv("OBS_REGION"))
	if region != "" {
		return region, bucket
	}
	host := strings.TrimSpace(baseURL)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(strings.SplitN(host, "/", 2)[0], ".")
	if parts := strings.Split(host, "."); len(parts) >= 2 {
		return parts[1], bucket
	}
	return "", bucket
}
