// Command evolution-space serves private workers and an optional separate browser gateway.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"aster/evolution-space/internal/httpapi"
	"aster/evolution-space/internal/identity"
	"aster/evolution-space/internal/modelconn"
	"aster/evolution-space/internal/modelconn/native"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/studioclient"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	browserListen := flag.String("browser-listen", "", "Optional loopback browser gateway behind the shared TLS front door")
	publicOrigin := flag.String("public-origin", "", "Canonical shared HTTPS origin for browser identity")
	frontend := flag.String("frontend", "", "Evolution Space frontend origin")
	listen := flag.String("listen", "127.0.0.1:18484", "Private loopback listener; use an authenticated TLS proxy for remote access")
	directory := flag.String("state-directory", "", "Absolute application-owned state directory")
	endpoint := flag.String("identity-endpoint", "", "Central private identity origin")
	clientSecretEnv := flag.String("identity-secret-env", "EVOLUTION_SPACE_IDENTITY_SECRET", "Environment variable containing registered service credential")
	centralSecretEnv := flag.String("central-secret-env", "EVOLUTION_SPACE_CENTRAL_SECRET", "Environment variable containing inbound central credential")
	evaluationConfigPath := flag.String("evaluation-connections", "", "Optional private JSON evaluator connections with credential environment references")
	studioSecretEnv := flag.String("studio-secret-env", "EVOLUTION_SPACE_STUDIO_SECRET", "Environment variable containing inbound Studio evaluation credential")
	studioInputs := flag.String("studio-input-endpoint", "", "Private Studio participant origin for assigned content reads")
	studioInputSecretEnv := flag.String("studio-input-secret-env", "STUDIO_EVOLUTION_SPACE_INPUT_SECRET", "Environment with outbound Studio input credential")
	nativePath := flag.String("native-config", "", "Optional private native model/implementation registry JSON")
	modelKeyEnv := flag.String("model-key-secret-env", "EVOLUTION_SPACE_MODEL_KEY_SECRET", "Environment variable with the host secret that encrypts model connection keys")
	flag.Parse()
	if (*browserListen != "" || *publicOrigin != "" || *frontend != "") && (*browserListen == "" || *publicOrigin == "" || *frontend == "") {
		return fmt.Errorf("browser-listen, public-origin and frontend are required together")
	}
	if !filepath.IsAbs(*directory) || *directory == "/" {
		return fmt.Errorf("absolute state directory required")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("private listener requires loopback address")
	}
	authority, err := identity.NewProjectionAuthority(identity.ProjectionConfig{Endpoint: *endpoint, ClientID: "evolution-space", ClientSecret: os.Getenv(*clientSecretEnv), CentralSecret: os.Getenv(*centralSecretEnv)})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := projections.Open(ctx, filepath.Join(*directory, "projections.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	var evaluationHandler, providerInputHandler, resultArtifactHandler, generationHandler http.Handler
	var evaluationDone chan struct{}
	if *evaluationConfigPath != "" {
		connections, err := readEvaluationConnections(*evaluationConfigPath)
		if err != nil {
			return err
		}
		api, err := httpapi.NewEvaluationAPI(store, authority, os.Getenv(*studioSecretEnv))
		if err != nil {
			return err
		}
		evaluationHandler = api.Handler()
		resultAPI, err := httpapi.NewRunResultArtifactAPI(store, authority, os.Getenv(*studioSecretEnv))
		if err != nil {
			return err
		}
		resultArtifactHandler = resultAPI.Handler()
		content, err := studioclient.New(*studioInputs, os.Getenv(*studioInputSecretEnv))
		if err != nil {
			return err
		}
		credentials := map[string]string{}
		for id, connection := range connections {
			credentials[id] = connection.InputSecret
		}
		inputAPI, err := httpapi.NewEvaluationInputAPI(store, authority, content, credentials)
		if err != nil {
			return err
		}
		providerInputHandler = inputAPI.Handler()

		workerCtx, cancel := context.WithCancel(ctx)
		evaluationDone = make(chan struct{})
		go func() { defer close(evaluationDone); runEvaluations(workerCtx, store, authority, connections) }()
		defer func() { cancel(); <-evaluationDone }()
	}
	browserOptions := httpapi.BrowserOptions{}
	if *nativePath != "" {
		registry, closeRegistry, config, err := readNativeRegistryConfig(*nativePath, store)
		if err != nil {
			return err
		}
		defer closeRegistry()
		browserOptions = httpapi.BrowserOptions{Models: registry.ModelRevisions(), ExampleBuilders: len(registry.Transforms) > 0}
		if config.ModelConnections {
			connections, err := modelconn.Open(ctx, filepath.Join(*directory, "modelconn.db"), os.Getenv(*modelKeyEnv))
			if err != nil {
				return err
			}
			defer connections.Close()
			schemas := registry.Schemas
			adapter := native.NewAdapter(connections, store, func(digest string) (json.RawMessage, bool) {
				raw, ok := schemas[digest]
				return raw, ok
			})
			registry.Source = adapter
			browserOptions.Connections = adapter
		}
		studio, err := studioclient.New(*studioInputs, os.Getenv(*studioInputSecretEnv))
		if err != nil {
			return err
		}
		api, err := httpapi.NewGenerationAPI(store, authority, os.Getenv(*centralSecretEnv))
		if err != nil {
			return err
		}
		generationHandler = api.Handler()
		resultAPI, err := httpapi.NewRunResultArtifactAPI(store, authority, os.Getenv(*studioSecretEnv))
		if err != nil {
			return err
		}
		resultArtifactHandler = resultAPI.Handler()
		workerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); runNative(workerCtx, store, authority, registry, studio) }()
		defer func() { cancel(); <-done }()
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	projectionHandler := (httpapi.ProjectionAPI{Store: store, Authority: authority}).Handler()
	catalogHandler := (httpapi.CatalogAPI{Store: store, Authority: authority}).Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/evolution-space/healthz" || r.URL.Path == "/evolution-space/readyz" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if generationHandler != nil && strings.HasPrefix(r.URL.Path, "/evolution-space/internal/v1/generation/") {
			generationHandler.ServeHTTP(w, r)
			return
		}
		if resultArtifactHandler != nil && r.URL.Path == "/evolution-space/internal/v1/run-result-artifact" {
			resultArtifactHandler.ServeHTTP(w, r)
			return
		}
		if providerInputHandler != nil && strings.HasPrefix(r.URL.Path, "/evolution-space/provider/v1/") {
			providerInputHandler.ServeHTTP(w, r)
			return
		}
		if evaluationHandler != nil && strings.HasPrefix(r.URL.Path, "/evolution-space/api/v1/evaluations/") {
			evaluationHandler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/evolution-space/api/v1/catalog/") {
			catalogHandler.ServeHTTP(w, r)
			return
		}
		projectionHandler.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	finished := make(chan error, 2)
	var browser *http.Server
	if *browserListen != "" {
		var browserListener net.Listener
		browser, browserListener, err = browserServer(store, *browserListen, *publicOrigin, *frontend, *endpoint, os.Getenv(*clientSecretEnv), browserOptions)
		if err != nil {
			return err
		}
		defer browserListener.Close()
		go func() { finished <- browser.Serve(browserListener) }()
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if browser != nil {
			if browser.Shutdown(shutdown) != nil {
				_ = browser.Close()
			}
		}
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return nil // The shared deferred shutdown drains both listeners before closing storage.
	}
}
