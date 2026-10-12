package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/httpapi"
	"volume-studio/backend/internal/identity"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8082", "private Studio gateway listener")
	directory := flag.String("state-directory", "", "absolute private persistent Studio directory")
	issuer := flag.String("public-origin", "", "public HTTPS origin and central issuer")
	endpoint := flag.String("identity-endpoint", "", "private central identity origin")
	clientID := flag.String("identity-client", "studio", "registered central client ID")
	secretEnv := flag.String("identity-secret-env", "STUDIO_IDENTITY_CLIENT_SECRET", "environment containing the registered client credential")
	frontend := flag.String("frontend", "http://127.0.0.1:3002", "independent Studio frontend origin")
	publisher := flag.String("publisher-id", "", "stable publisher instance ID retained through restore")
	quota := flag.Int64("quota-bytes", 8<<30, "Studio blob and upload reservation budget")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("Studio gateway requires a private loopback listener behind the HTTPS front door")
	}
	if !filepath.IsAbs(*directory) || *directory == "/" {
		log.Fatal("absolute private state-directory required")
	}
	client, err := identity.NewClient(identity.Config{Issuer: *issuer, Endpoint: *endpoint, ClientID: *clientID, ClientSecret: os.Getenv(*secretEnv)})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sessions, err := identity.OpenSessions(ctx, filepath.Join(*directory, "sessions.db"), client)
	if err != nil {
		log.Fatal(err)
	}
	defer sessions.Close()
	store, err := authoring.Open(ctx, authoring.Config{Directory: *directory, PublisherID: *publisher, QuotaBytes: *quota})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	api := httpapi.NewAuthoringAPI(store)
	handler, err := (httpapi.Gateway{Sessions: sessions, PublicOrigin: *issuer, Frontend: *frontend, API: api.Serve}).Handler()
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = store.ExpireUploads(ctx)
				_, err := store.ValidateNext(ctx, func(ctx context.Context, actor authoring.Actor) error {
					if actor.Issuer != *issuer {
						return identity.ErrUnauthorized
					}
					_, err := client.Principal(ctx, actor.Subject)
					return err
				})
				if err != nil && !errors.Is(err, context.Canceled) {
					log.Print("publication awaiting integrity or principal check; inspect operation state")
				}
				_, _ = store.Collect(ctx)
			}
		}
	}()
	defer func() { stop(); workers.Wait() }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
