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
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	projectionHandler := (httpapi.ProjectionAPI{Store: store, Authority: authority}).Handler()
	catalogHandler := (httpapi.CatalogAPI{Store: store, Authority: authority}).Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
