package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/servicegateway"
)

// GatewayApplication owns only HTTP forwarding and protected control. It opens
// no Store/engine and has no dependency on the owner process's context/lifetime.
type GatewayApplication struct {
	config                Config
	logger                *slog.Logger
	gateways              []*servicegateway.Gateway
	authorities           []*servicegateway.RemoteAuthority
	servers               []*http.Server
	listeners             []net.Listener
	control               *http.Server
	unlock                func()
	mu                    sync.Mutex
	active                int
	stopping              bool
	handlers, serversDone sync.WaitGroup
	stop                  chan struct{}
	stopOnce, closeOnce   sync.Once
}

func (c Config) GatewaySocket() string { return filepath.Join(c.StateDirectory, "gateway.sock") }

func NewGatewayApplication(ctx context.Context, cfg Config, logger *slog.Logger) (app *GatewayApplication, result error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Gateways) == 0 {
		return nil, errors.New("no service gateways configured")
	}
	if os.Geteuid() == 0 {
		return nil, errors.New("Project gateways require a non-engine unprivileged UID matching engine --via-uid")
	}
	if logger == nil {
		logger = slog.Default()
	}
	app = &GatewayApplication{config: cfg, logger: logger, stop: make(chan struct{})}
	defer func() {
		if result != nil {
			app.Close()
		}
	}()
	var err error
	app.unlock, err = servicegateway.LockProcess(cfg.StateDirectory)
	if err != nil {
		return app, err
	}
	for _, setting := range cfg.Gateways {
		secret := os.Getenv(setting.APIKeyEnv)
		if setting.APIKeyEnv != "" && secret == "" {
			return app, fmt.Errorf("gateway secret environment variable %s is empty", setting.APIKeyEnv)
		}
		listener, err := net.Listen("tcp", setting.Listen)
		if err != nil {
			return app, err
		}
		app.listeners = append(app.listeners, listener)
		authority, err := servicegateway.NewRemoteAuthority(ctx, cfg.ControlSocket, setting.Audience, setting.MaxRequests)
		if err != nil {
			return app, err
		}
		app.authorities = append(app.authorities, authority)
		gateway, err := servicegateway.New(authority, servicegateway.Config{Audience: setting.Audience, Upstream: setting.Upstream, APIKey: secret, MaxRequests: setting.MaxRequests})
		if err != nil {
			return app, err
		}
		app.gateways = append(app.gateways, gateway)
		app.servers = append(app.servers, app.server(app.track(gateway)))
	}
	listener, err := listenControl(cfg.GatewaySocket())
	if err != nil {
		return app, err
	}
	app.listeners = append(app.listeners, listener)
	app.control = app.server(app.controlHandler())
	app.servers = append(app.servers, app.control)
	return app, nil
}

func (a *GatewayApplication) server(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
}

func (a *GatewayApplication) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		if a.stopping {
			a.mu.Unlock()
			http.Error(w, "gateway stopping", 503)
			return
		}
		a.active++
		a.handlers.Add(1)
		a.mu.Unlock()
		defer func() { a.mu.Lock(); a.active--; a.mu.Unlock(); a.handlers.Done() }()
		next.ServeHTTP(w, r)
	})
}

func (a *GatewayApplication) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		pending := int64(0)
		for _, authority := range a.authorities {
			pending += authority.Pending()
		}
		checks := map[string]any{}
		registrations := map[string]product.GatewayRegistration{}
		for n, gateway := range a.gateways {
			checks[a.config.Gateways[n].Audience] = gateway.AuthorityStats()
			registrations[a.config.Gateways[n].Audience] = a.authorities[n].Registration()
		}
		writeControl(w, map[string]any{"registrations": registrations, "authority_checks": checks, "pid": os.Getpid(), "active": a.active, "pending": pending, "stopping": a.stopping, "authority_protocol": servicegateway.AuthorityVersion, "owner_handoff_protocol": product.OwnerHandoffProtocol})
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner      string `json:"owner"`
			Generation int64  `json:"generation"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		if body.Owner == "" || body.Generation < 1 {
			http.Error(w, "release owner and generation required", 400)
			return
		}
		// A gateway-only update still closes all execution admission and waits for
		// admitted turns, including their possible next upstream requests, to end.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var reply bytes.Buffer
		if err := Control(ctx, a.config.ControlSocket, "POST", "/release/ready", body, &reply); err != nil {
			http.Error(w, "application release is not quiescent", 409)
			return
		}
		var state product.ReleaseState
		if json.Unmarshal(reply.Bytes(), &state) != nil || state.Phase != "ready" || state.Mode != "quiescent" || state.Owner != body.Owner || state.Generation != body.Generation {
			http.Error(w, "invalid release checkpoint", 409)
			return
		}
		a.mu.Lock()
		pending := int64(0)
		for _, authority := range a.authorities {
			pending += authority.Pending()
		}
		if a.active != 0 || pending != 0 {
			a.mu.Unlock()
			http.Error(w, "gateway requests still settling", 409)
			return
		}
		a.stopping = true
		a.mu.Unlock()
		writeControl(w, map[string]string{"phase": "stopping"})
		a.stopOnce.Do(func() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = a.control.Shutdown(ctx)
				close(a.stop)
			}()
		})
	})
	return mux
}

func (a *GatewayApplication) Run(ctx context.Context) error {
	failures := make(chan error, len(a.servers))
	for index, server := range a.servers {
		a.serversDone.Add(1)
		go func() {
			defer a.serversDone.Done()
			var err error
			if index < len(a.config.Gateways) && a.config.Gateways[index].TLSCert != "" {
				setting := a.config.Gateways[index]
				err = server.ServeTLS(a.listeners[index], setting.TLSCert, setting.TLSKey)
			} else {
				err = server.Serve(a.listeners[index])
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				failures <- err
			}
		}()
	}
	a.logger.Info("Project gateway ready", "control", a.config.GatewaySocket(), "audiences", len(a.gateways))
	defer a.Close()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stop:
		return nil
	case err := <-failures:
		return err
	}
}

func (a *GatewayApplication) Close() {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.stopping = true
		a.mu.Unlock()
		for _, gateway := range a.gateways {
			gateway.Close()
		}
		for _, server := range a.servers {
			_ = server.Close()
		}
		for _, listener := range a.listeners {
			_ = listener.Close()
		}
		a.serversDone.Wait()
		a.handlers.Wait()
		for _, authority := range a.authorities {
			authority.Close()
		}
		if a.unlock != nil {
			a.unlock()
		}
	})
}
