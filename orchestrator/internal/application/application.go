package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/servicegateway"
)

type Application struct {
	Config         Config
	Store          *product.Store
	Backend        *khora.Backend
	Lifecycle      *product.Lifecycle
	Agents         *product.Agents
	Terminals      *product.Terminals
	Files          *product.Files
	Shares         *product.Shares
	Captures       *product.Captures
	Auth           *producthttp.Authentication
	Workers        *product.Workers
	BrowserAddress string
	logger         *slog.Logger
	API            *producthttp.Server
	ctx            context.Context
	cancel         context.CancelFunc
	group          sync.WaitGroup
	servers        []*http.Server
	listeners      []net.Listener
	gateways       []*servicegateway.Gateway
	publication    sync.Mutex
	closeOnce      sync.Once
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
	if err := cfg.Validate(); err != nil {
		return app, err
	}
	manifestFile, err := os.Open(cfg.Template.Manifest)
	if err != nil {
		return app, err
	}
	cfg.Manifest, err = runtimerelease.Load(manifestFile, cfg.Template.Digest)
	manifestFile.Close()
	if err != nil {
		return app, err
	}
	app.Config = cfg

	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return app, err
	}
	store, err := product.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return app, err
	}
	app.Store = store
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
	template, err := client.Template(startup, cfg.Template.ID)
	if err != nil {
		return app, err
	}
	if template.Manifest.Platform != "" && template.Manifest.Platform != cfg.Manifest.Platform {
		return app, errors.New("engine template platform differs from runtime release")
	}
	if err = store.ValidateReleasedBindings(startup, cfg.Engine.StoreID); err != nil {
		return app, err
	}
	profile, err := app.publish(startup)
	if err != nil {
		return app, err
	}
	lifecycle := &product.Lifecycle{Store: store, Backend: backend, MaxProjects: cfg.Limits.Projects, VerifyRuntime: backend.VerifyRuntime}
	app.Lifecycle = lifecycle
	agents := product.NewAgents(ctx, store, lifecycle, backend, profile, cfg.Limits.Agents)
	agents.MaxPerProject = cfg.Limits.AgentsPerProject
	agents.ServiceAudience = cfg.AgentAudience
	app.Agents = agents
	terminals := product.NewTerminals(ctx, store, lifecycle, backend, profile, cfg.Limits.Terminals)
	terminals.MaxPerProject = cfg.Limits.TerminalsPerProject
	app.Terminals = terminals
	files := &product.Files{Store: store, Lifecycle: lifecycle, Backend: backend, Profile: profile}
	app.Files = files
	shares := &product.Shares{Store: store, Files: files, Directory: filepath.Join(cfg.StateDirectory, "shares")}
	app.Shares = shares
	captures := &product.Captures{Store: store, Backend: backend, Directory: filepath.Join(cfg.StateDirectory, "native")}
	app.Captures = captures
	lifecycle.Artifacts = &product.Artifacts{Store: store, Captures: captures, Shares: shares}
	recovery := &product.AgentRecovery{Store: store, Lifecycle: lifecycle, Backend: backend}
	retirement := &product.ChildRetirement{Store: store, Lifecycle: lifecycle, Backend: backend, Captures: captures, Shares: shares}
	app.Workers = &product.Workers{Store: store, Lifecycle: lifecycle, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Captures: captures, Recovery: recovery, Retirement: retirement, Titles: cfg.titleGenerator(), IdleAfter: time.Duration(cfg.Limits.IdleAfter), IdleAgentAfter: time.Duration(cfg.Limits.IdleAgentAfter), Interval: time.Duration(cfg.Limits.Interval), Report: func(kind string, err error) { logger.Warn("application work pending", "scope", kind, "error", err) }}
	var users []producthttp.AuthUser
	if cfg.Auth.UsersFile != "" {
		users, err = ReadUsers(cfg.Auth.UsersFile)
		if err != nil {
			return app, err
		}
	}
	transportConfig := producthttp.Config{TrustForwardedHost: true}
	if cfg.HTTPMetrics {
		transportConfig.ObserveHTTP = func(o producthttp.HTTPObservation) {
			logger.Info("http timing", "category", o.Category, "status", o.Status, "bytes", o.Bytes, "duration_ms", o.Duration.Milliseconds())
		}
	}
	app.Auth, err = producthttp.NewAuthentication(store, transportConfig, users, cfg.Auth.DevelopmentActor)
	if err != nil {
		return app, err
	}
	app.API = &producthttp.Server{Store: store, Lifecycle: lifecycle, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Profile: profile, Auth: app.Auth, Config: transportConfig}
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
	applicationURL, _ := url.Parse("http://project-application")
	proxy, err := edge.New(edge.Config{Application: applicationURL, ApplicationSocket: cfg.ApplicationSocket, Frontend: frontendURL})
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
	app.servers = append(app.servers, app.server(app.controlHandler()))
	if len(cfg.Gateways) > 0 && os.Geteuid() == 0 {
		return app, errors.New("Project gateways require a non-engine application UID; run the application unprivileged and configure engine --via-uid accordingly")
	}
	for _, setting := range cfg.Gateways {
		secret := os.Getenv(setting.APIKeyEnv)
		if setting.APIKeyEnv != "" && secret == "" {
			return app, fmt.Errorf("gateway secret environment variable %s is empty", setting.APIKeyEnv)
		}
		gateway, err := servicegateway.New(store, servicegateway.Config{Audience: setting.Audience, Upstream: setting.Upstream, APIKey: secret, MaxRequests: setting.MaxRequests})
		if err != nil {
			return app, err
		}
		listener, err := net.Listen("tcp", setting.Listen)
		if err != nil {
			gateway.Close()
			return app, err
		}
		app.gateways = append(app.gateways, gateway)
		app.listeners = append(app.listeners, listener)
		app.servers = append(app.servers, app.server(gateway))
	}
	return app, nil
}
func (a *Application) server(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return a.ctx }}
}
func (a *Application) publish(ctx context.Context) (product.Profile, error) {
	a.publication.Lock()
	defer a.publication.Unlock()
	profile := a.Config.Profile()
	publisher := content.Publisher{Repository: a.Store, Backend: a.Backend}
	for _, source := range []struct{ path, purpose, destination string }{{a.Config.Content.AgentConfig, "config", "/opt/agent-config"}, {a.Config.Content.SharedAssets, "assets", "/opt/shared-assets"}} {
		if source.path == "" {
			continue
		}
		revision, err := content.Read(source.path)
		if err != nil {
			return profile, err
		}
		volume, err := publisher.Publish(ctx, source.purpose, revision, a.Config.Content.VolumeBytes)
		if err != nil {
			return profile, err
		}
		profile.PublishedMounts = append(profile.PublishedMounts, sandbox.Mount{VolumeID: volume, Destination: source.destination, Access: "shared-ro"})
	}
	return profile, profile.Validate()
}
func (a *Application) Run(ctx context.Context) error {
	a.group.Add(1)
	go func() { defer a.group.Done(); a.Workers.Run(a.ctx) }()
	a.group.Add(1)
	go func() { defer a.group.Done(); a.watchCredentials() }()
	failures := make(chan error, len(a.servers))
	for index, server := range a.servers {
		index, server := index, server
		a.group.Add(1)
		go func() {
			defer a.group.Done()
			var err error
			if index == 1 && a.Config.TLSCert != "" {
				err = server.ServeTLS(a.listeners[index], a.Config.TLSCert, a.Config.TLSKey)
			} else if index >= 3 && a.Config.Gateways[index-3].TLSCert != "" {
				setting := a.Config.Gateways[index-3]
				err = server.ServeTLS(a.listeners[index], setting.TLSCert, setting.TLSKey)
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
		for _, gateway := range a.gateways {
			gateway.Close()
		}
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
		if a.Store != nil {
			_ = a.Store.Close()
		}
	})
}
func (a *Application) watchCredentials() {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	lastError := ""
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
		}
		a.Auth.GC()
		if a.Config.Auth.UsersFile == "" {
			continue
		}
		users, err := ReadUsers(a.Config.Auth.UsersFile)
		if err == nil {
			err = a.Auth.ReplaceUsers(users)
		}
		if err != nil {
			if err.Error() != lastError {
				a.logger.Warn("credential reload refused; previous valid registry retained", "error", err)
				lastError = err.Error()
			}
		} else {
			lastError = ""
		}
	}
}
