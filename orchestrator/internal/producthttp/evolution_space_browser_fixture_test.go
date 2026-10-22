package producthttp

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

// Disposable real central authority for Evolution Space's public browser suite.
func TestEvolutionSpaceBrowserIdentityFixture(t *testing.T) {
	if os.Getenv("EVOLUTION_SPACE_IDENTITY_BROWSER_FIXTURE") != "1" {
		t.Skip("explicit Evolution Space browser fixture only")
	}
	origin := os.Getenv("EVOLUTION_SPACE_BROWSER_ORIGIN")
	if origin == "" || len(os.Getenv("EVOLUTION_SPACE_BROWSER_CLIENT_SECRET")) < 32 {
		t.Fatal("fixture authority configuration missing")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	must(t, err)
	defer store.Close()
	_, err = store.BootstrapAccount(ctx, "evolution-space-admin", "browser-fixture-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "evolution-space-admin", "browser-fixture-password")
	must(t, err)
	for _, name := range []string{"Team Alpha", "Team Beta", "Secret Team"} {
		_, err = store.SaveGroup(ctx, token, "", product.GroupChange{RequestID: strings.ReplaceAll(name, " ", "-"), Name: &name})
		must(t, err)
	}
	cfg := &identityconfig.Config{Issuer: origin, Listen: "127.0.0.1:18583", Clients: []identityconfig.Client{{ID: "evolution-space", Callback: origin + "/evolution-space/auth/callback", SecretEnvs: []string{"EVOLUTION_SPACE_BROWSER_CLIENT_SECRET"}}}}
	must(t, cfg.CheckCredentials())
	auth := NewAuthentication(store, Config{Identity: cfg, TrustForwardedHost: true})
	for addr, handler := range map[string]http.Handler{"127.0.0.1:18581": auth.Handler(http.NotFoundHandler()), "127.0.0.1:18583": auth.IdentityServiceHandler()} {
		listener, err := net.Listen("tcp", addr)
		must(t, err)
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		defer server.Close()
		go func() { _ = server.Serve(listener) }()
	}
	<-ctx.Done()
}
