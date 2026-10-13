package producthttp

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

// This test executable is a disposable real central identity authority for the
// cross-application browser gate. It is never included in a release artifact.
func TestStudioBrowserIdentityFixture(t *testing.T) {
	if os.Getenv("STUDIO_IDENTITY_BROWSER_FIXTURE") != "1" {
		t.Skip("explicit Studio browser fixture only")
	}
	origin := os.Getenv("STUDIO_BROWSER_ORIGIN")
	secret := os.Getenv("STUDIO_BROWSER_CLIENT_SECRET")
	if origin == "" || len(secret) < 32 {
		t.Fatal("fixture authority configuration missing")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "studio-admin", "browser-fixture-password", identitySeed(t))
	must(t, err)
	cfg := &identityconfig.Config{Issuer: origin, Listen: "127.0.0.1:18483", Clients: []identityconfig.Client{{ID: "studio", Callback: origin + "/volumes-studio/auth/callback", SecretEnvs: []string{"STUDIO_BROWSER_CLIENT_SECRET"}}}}
	must(t, cfg.CheckCredentials())
	auth := NewAuthentication(store, Config{Identity: cfg, TrustForwardedHost: true})
	public := auth.Handler(http.NotFoundHandler())
	for addr, handler := range map[string]http.Handler{"127.0.0.1:18481": public, "127.0.0.1:18483": auth.IdentityServiceHandler()} {
		listener, err := net.Listen("tcp", addr)
		must(t, err)
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		defer server.Close()
		go func() { _ = server.Serve(listener) }()
	}
	<-ctx.Done()
}
