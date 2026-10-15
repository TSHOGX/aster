// Command evaluation-space serves the private configuration participant API.
package main

import (
	"context"
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

	"aster/evaluation-space/internal/httpapi"
	"aster/evaluation-space/internal/identity"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/studioclient"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:18484", "Private loopback listener; use an authenticated TLS proxy for remote access")
	directory := flag.String("state-directory", "", "Absolute application-owned state directory")
	endpoint := flag.String("identity-endpoint", "", "Central private identity origin")
	clientSecretEnv := flag.String("identity-secret-env", "EVALUATION_SPACE_IDENTITY_SECRET", "Environment variable containing registered service credential")
	centralSecretEnv := flag.String("central-secret-env", "EVALUATION_SPACE_CENTRAL_SECRET", "Environment variable containing inbound central credential")
	evaluationConfigPath := flag.String("evaluation-connections", "", "Optional private JSON evaluator connections with credential environment references")
	studioSecretEnv := flag.String("studio-secret-env", "EVALUATION_SPACE_STUDIO_SECRET", "Environment variable containing inbound Studio evaluation credential")
	studioInputs := flag.String("studio-input-endpoint", "", "Private Studio participant origin for assigned content reads")
	studioInputSecretEnv := flag.String("studio-input-secret-env", "STUDIO_EVALUATION_INPUT_SECRET", "Environment with outbound Studio input credential")
	nativePath := flag.String("native-config", "", "Optional private native model/implementation registry JSON")
	flag.Parse()
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
	authority, err := identity.NewProjectionAuthority(identity.ProjectionConfig{Endpoint: *endpoint, ClientID: "evaluation-space", ClientSecret: os.Getenv(*clientSecretEnv), CentralSecret: os.Getenv(*centralSecretEnv)})
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
	if *nativePath != "" {
		registry, closeRegistry, err := readNativeRegistry(*nativePath, store)
		if err != nil {
			return err
		}
		defer closeRegistry()
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
		if generationHandler != nil && strings.HasPrefix(r.URL.Path, "/evaluation-space/internal/v1/generation/") {
			generationHandler.ServeHTTP(w, r)
			return
		}
		if resultArtifactHandler != nil && r.URL.Path == "/evaluation-space/internal/v1/run-result-artifact" {
			resultArtifactHandler.ServeHTTP(w, r)
			return
		}
		if providerInputHandler != nil && strings.HasPrefix(r.URL.Path, "/evaluation-space/provider/v1/") {
			providerInputHandler.ServeHTTP(w, r)
			return
		}
		if evaluationHandler != nil && strings.HasPrefix(r.URL.Path, "/evaluation-space/api/v1/evaluations/") {
			evaluationHandler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/evaluation-space/api/v1/catalog/") {
			catalogHandler.ServeHTTP(w, r)
			return
		}
		projectionHandler.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		err := <-finished
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
