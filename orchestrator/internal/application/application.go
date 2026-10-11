package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/edge"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/producthttp"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

type Application struct {
	Config          Config
	Store           *product.Store
	Backend         *khora.Backend
	Lifecycle       *product.Lifecycle
	Agents          *product.Agents
	Terminals       *product.Terminals
	Files           *product.Files
	Shares          *product.Shares
	Captures        *product.Captures
	Auth            *producthttp.Authentication
	Workers         *product.Workers
	BrowserAddress  string
	logger          *slog.Logger
	API             *producthttp.Server
	ctx             context.Context
	cancel          context.CancelFunc
	group           sync.WaitGroup
	servers         []*http.Server
	listeners       []net.Listener
	publication     sync.Mutex
	closeOnce       sync.Once
	controlServer   *http.Server
	releaseIdentity ReleaseIdentity
	retireOnce      sync.Once
}

func New(ctx context.Context, cfg Config, logger *slog.Logger) (app *Application, result error) {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	app = &Application{Config: cfg, logger: logger, ctx: ctx, cancel: cancel}
	defer func() {
		if result != nil {
			app.Close()
		}
	}()
	prepared, err := prepareRelease(cfg)
	if err != nil {
		return app, err
	}
	cfg = prepared.config
	app.Config = cfg
	app.releaseIdentity = prepared.identity

	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return app, err
	}
	store, err := product.OpenApplication(ctx, cfg.DatabasePath(), product.ApplicationIdentity{ExecutableDigest: app.releaseIdentity.ExecutableDigest, CompatibilityDigest: app.releaseIdentity.CompatibilityDigest})
	if err != nil {
		return app, err
	}
	app.Store = store
	if err := store.ConfigureNotificationPolicy(ctx, product.NotificationPolicy{
		AgentRunOutcome:    cfg.Notifications.AgentRunOutcome,
		ConversationShared: cfg.Notifications.ConversationShared,
		OperatorDirected:   cfg.Notifications.OperatorDirected,
		OperatorBroadcast:  cfg.Notifications.OperatorBroadcast,
	}); err != nil {
		return app, err
	}
	engineConfig, err := cfg.Engine.Build(cfg.base)
	if err != nil {
		return app, err
	}
	startup, done := context.WithTimeout(ctx, 2*time.Minute)
	defer done()
	backend, err := khora.New(startup, engineConfig)
	if err != nil {
		return app, err
	}
	app.Backend = backend
	client, err := sdk.New(engineConfig.Endpoint, sdk.WithHTTPClient(engineConfig.HTTPClient))
	if err != nil {
		return app, err
	}
	registered, err := client.Template(startup, cfg.Template.ID)
	if err != nil {
		return app, err
	}
	if registered.Manifest.Platform != "" && registered.Manifest.Platform != cfg.Manifest.Platform {
		return app, errors.New("engine template platform differs from runtime release")
	}
	if err = store.ValidateReleasedBindings(startup, cfg.Engine.StoreID); err != nil {
		return app, err
	}
	if err = store.RegisterSandbox(startup, product.RegisteredSandbox{Reference: product.ArtifactReference{ID: cfg.Template.ID, Version: cfg.Template.Version, Digest: cfg.Template.Digest}, StoreID: cfg.Engine.StoreID, TemplateID: cfg.Template.ID, Manifest: cfg.Manifest}); err != nil {
		return app, err
	}
	bindings, err := app.publishPrepared(startup, prepared.publications)
	if err != nil {
		return app, err
	}
	lifecycle := &product.Lifecycle{Store: store, Backend: backend, MaxProjects: cfg.Limits.Projects, VerifyRuntime: backend.VerifyRuntime}
	app.Lifecycle = lifecycle
	agents := product.NewAgents(ctx, store, lifecycle, backend, bindings, cfg.Limits.Agents)
	agents.MaxPerProject = cfg.Limits.AgentsPerProject
	agents.ServiceAudience = cfg.AgentAudience
	agents.PlatformEndpoint = func(ctx context.Context, scope product.Scope) string {
		return app.platformEndpoint(ctx, scope)
	}
	app.Agents = agents
	terminals := product.NewTerminals(ctx, store, lifecycle, backend, bindings, cfg.Limits.Terminals)
	terminals.MaxPerProject = cfg.Limits.TerminalsPerProject
	app.Terminals = terminals
	readDirectory := filepath.Join(cfg.StateDirectory, "read-captures")
	if err := os.MkdirAll(readDirectory, 0700); err != nil {
		return app, err
	}
	files := &product.Files{Store: store, Lifecycle: lifecycle, Backend: backend, Bindings: bindings, CaptureDirectory: readDirectory}
	app.Files = files
	shares := &product.Shares{Store: store, Files: files, Directory: filepath.Join(cfg.StateDirectory, "shares")}
	app.Shares = shares
	captures := &product.Captures{Store: store, Backend: backend, Directory: filepath.Join(cfg.StateDirectory, "native")}
	app.Captures = captures
	lifecycle.Artifacts = &product.Artifacts{Store: store, Captures: captures, Shares: shares}
	recovery := &product.AgentRecovery{Store: store, Lifecycle: lifecycle, Backend: backend}
	retirement := &product.ChildRetirement{Store: store, Lifecycle: lifecycle, Backend: backend, Captures: captures, Shares: shares}
	app.Workers = &product.Workers{Store: store, Lifecycle: lifecycle, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Captures: captures, Recovery: recovery, Retirement: retirement, Titles: cfg.titleGenerator(), IdleAfter: time.Duration(cfg.Limits.IdleAfter), IdleAgentAfter: time.Duration(cfg.Limits.IdleAgentAfter), Interval: time.Duration(cfg.Limits.Interval), Report: func(kind string, err error) { logger.Warn("application work pending", "scope", kind, "error", err) }}
	transportConfig := producthttp.Config{TrustForwardedHost: true}
	if cfg.HTTPMetrics {
		transportConfig.ObserveHTTP = func(o producthttp.HTTPObservation) {
			logger.Info("http timing", "category", o.Category, "status", o.Status, "bytes", o.Bytes, "duration_ms", o.Duration.Milliseconds())
		}
	}
	app.Auth = producthttp.NewAuthentication(store, transportConfig)
	app.API = &producthttp.Server{Store: store, Lifecycle: lifecycle, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Bindings: bindings, Auth: app.Auth, Config: transportConfig}
	api := app.API.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	})
	mux.Handle("/", app.Auth.Handler(api))
	applicationListener, err := listenControl(cfg.ApplicationSocket)
	if err != nil {
		return app, err
	}
	app.listeners = append(app.listeners, applicationListener)
	app.servers = append(app.servers, app.server(mux))
	frontendURL, _ := url.Parse(cfg.Frontend)
	adminURL, _ := url.Parse(cfg.AdminFrontend)
	applicationURL, _ := url.Parse("http://project-application")
	proxy, err := edge.New(edge.Config{Application: applicationURL, ApplicationSocket: cfg.ApplicationSocket, Frontend: frontendURL, AdminFrontend: adminURL, PageAccount: app.Auth.Actor})
	if err != nil {
		return app, err
	}
	browserListener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return app, err
	}
	app.BrowserAddress = browserListener.Addr().String()
	app.listeners = append(app.listeners, browserListener)
	app.servers = append(app.servers, app.server(proxy))
	control, err := listenControl(cfg.ControlSocket)
	if err != nil {
		return app, err
	}
	app.listeners = append(app.listeners, control)
	app.controlServer = app.server(app.controlHandler())
	app.servers = append(app.servers, app.controlServer)
	if len(cfg.Gateways) > 0 && os.Geteuid() == 0 {
		return app, errors.New("Project gateways require a non-engine application UID; run the application unprivileged and configure engine --via-uid accordingly")
	}

	return app, nil
}
func (a *Application) server(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return a.ctx }}
}
func (a *Application) publish(ctx context.Context) (product.RuntimeBindings, error) {
	sources, err := readPublications(a.Config)
	if err != nil {
		return a.Config.RuntimeBindings(), err
	}
	return a.publishPrepared(ctx, sources)
}
func (a *Application) publishPrepared(ctx context.Context, sources []preparedPublication) (product.RuntimeBindings, error) {
	a.publication.Lock()
	defer a.publication.Unlock()
	bindings := a.Config.RuntimeBindings()
	publisher := content.Publisher{Repository: a.Store, Backend: a.Backend}
	for _, source := range sources {
		if _, err := publisher.Publish(ctx, source.purpose, source.revision, a.Config.Content.VolumeBytes); err != nil {
			return bindings, err
		}
		role := "shared_assets"
		if source.purpose == "config" {
			role = "agent_config"
		}
		artifact := product.BuiltinVolume{Reference: product.ArtifactReference{ID: role, Version: source.revision.Digest, Digest: source.revision.Digest}, StoreID: bindings.StoreID, PublicationPurpose: source.purpose, MinimumBytes: a.Config.Content.VolumeBytes, Roles: []string{role}, Entries: source.revision.Entries}
		if err := a.Store.RegisterBuiltinVolume(ctx, artifact); err != nil {
			return bindings, err
		}
	}
	return bindings, nil
}

func (a *Application) Run(ctx context.Context) error {
	a.group.Add(1)
	go func() { defer a.group.Done(); a.Workers.Run(a.ctx) }()
	a.group.Add(1)
	go func() { defer a.group.Done(); a.collectAuthenticationLimits() }()
	failures := make(chan error, len(a.servers))
	for index, server := range a.servers {
		index, server := index, server
		a.group.Add(1)
		go func() {
			defer a.group.Done()
			var err error
			if index == 1 && a.Config.TLSCert != "" {
				err = server.ServeTLS(a.listeners[index], a.Config.TLSCert, a.Config.TLSKey)
			} else {
				err = server.Serve(a.listeners[index])
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case failures <- err:
				default:
				}
			}
		}()
	}
	a.logger.Info("Project application ready", "browser", a.BrowserAddress, "control", a.Config.ControlSocket)
	select {
	case <-ctx.Done():
		a.Close()
		return nil
	case <-a.ctx.Done():
		a.Close()
		return nil
	case err := <-failures:
		a.Close()
		return err
	}
}
func (a *Application) Close() {
	a.closeOnce.Do(func() {
		a.cancel()
		for _, server := range a.servers {
			_ = server.Close()
		}
		for _, listener := range a.listeners {
			_ = listener.Close()
		}
		a.group.Wait()
		if a.Agents != nil {
			a.Agents.Close()
		}
		if a.Terminals != nil {
			a.Terminals.Close()
		}
		if a.API != nil {
			a.API.Close()
		}
		if a.Files != nil {
			_ = a.Files.Close()
		}
		if a.Store != nil {
			_ = a.Store.Close()
		}
	})
}

func (a *Application) retireAfterActivation() {
	a.retireOnce.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Finish the control response before closing the application. Other long
			// transports are closed by the normal lifecycle after their admission fence.
			_ = a.controlServer.Shutdown(ctx)
			a.cancel()
		}()
	})
}

func (a *Application) collectAuthenticationLimits() {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
			a.Auth.GC()
		}
	}
}
