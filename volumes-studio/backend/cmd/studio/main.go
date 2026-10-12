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
	"syscall"
	"time"

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
	handler, err := (httpapi.Gateway{Sessions: sessions, PublicOrigin: *issuer, Frontend: *frontend}).Handler()
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
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
