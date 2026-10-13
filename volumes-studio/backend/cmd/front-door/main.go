package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"volume-studio/backend/internal/frontdoor"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "HTTPS listener")
	public := flag.String("public-origin", "", "canonical public HTTPS origin")
	studio := flag.String("studio", "", "Studio gateway origin")
	application := flag.String("application", "", "Aster edge origin")
	cert := flag.String("cert", "", "TLS certificate PEM path")
	key := flag.String("key", "", "TLS private key PEM path")
	flag.Parse()
	if *cert == "" || *key == "" {
		log.Fatal("TLS certificate and key are required")
	}
	handler, err := frontdoor.New(frontdoor.Config{PublicOrigin: *public, Studio: *studio, Application: *application})
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	if err := server.ListenAndServeTLS(*cert, *key); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
