package application

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/product"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func networkReloadFixture(t *testing.T) (*Application, Config, string) {
	t.Helper()
	cfg, path := configurationFixture(t)
	writeNetworkConfig(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := product.Open(context.Background(), loaded.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, err := product.NewNetworkPolicy(loaded.Project.Reachability)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.InitializeNetworkPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	app := &Application{Config: loaded, Store: s, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return app, cfg, path
}
func writeNetworkConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkReloadAcceptsOnlyCompleteSupportedChanges(t *testing.T) {
	a, cfg, path := networkReloadFixture(t)
	ctx := context.Background()
	original, err := a.Store.NetworkDefaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Project.Reachability.Resolvers = []netip.Addr{netip.MustParseAddr("192.0.2.53")}
	cfg.Project.Reachability.Direct = []product.DirectRule{{CIDR: netip.MustParsePrefix("198.51.100.0/24"), Port: 443, Protocol: "tcp"}}
	writeNetworkConfig(t, path, cfg)
	report, err := a.ReloadNetworkPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Defaults.Revision == original.Revision || report.Reload.LastAttempt == "" || report.Reload.LastError != "" {
		t.Fatal(report)
	}
	accepted := report.Defaults
	// Repeated acceptance is a no-op, not a new durable revision/timestamp.
	report, err = a.ReloadNetworkPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Defaults.AcceptedAt != accepted.AcceptedAt {
		t.Fatal("unchanged defaults rewritten")
	}
	cfg.Limits.Agents++
	cfg.Project.Reachability.Resolvers = nil
	writeNetworkConfig(t, path, cfg)
	if _, err = a.ReloadNetworkPolicy(ctx); err == nil || !strings.Contains(err.Error(), "unsupported configuration change") {
		t.Fatal(err)
	}
	got, err := a.networkReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Defaults.Revision != accepted.Revision || got.Reload.LastError == "" {
		t.Fatal("partial candidate applied", got)
	}
	if err = os.WriteFile(path, []byte(`{"project":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReloadNetworkPolicy(ctx); err == nil {
		t.Fatal("partial JSON accepted")
	}
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("invalid startup file silently accepted")
	}
	got, err = a.networkReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Defaults.Revision != accepted.Revision {
		t.Fatal("last accepted policy lost")
	}
	cfg.Limits.Agents--
	writeNetworkConfig(t, path, cfg)
	report, err = a.ReloadNetworkPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Reload.LastError != "" || len(report.Defaults.Resolvers) != 0 {
		t.Fatal(report)
	}
}

func TestNetworkReloadControlAndAutomaticDetection(t *testing.T) {
	a, cfg, path := networkReloadFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	done := make(chan struct{})
	go func() { defer close(done); a.watchNetworkPolicy() }()
	defer func() { cancel(); <-done }()
	cfg.Project.Reachability.Resolvers = []netip.Addr{netip.MustParseAddr("192.0.2.53")}
	writeNetworkConfig(t, path, cfg)
	deadline := time.Now().Add(5 * time.Second)
	for {
		policy, err := a.Store.NetworkDefaults(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(policy.Resolvers) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic reload did not accept edit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfg.Project.Reachability.Resolvers = nil
	writeNetworkConfig(t, path, cfg)
	response := httptest.NewRecorder()
	a.controlHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/network/reload", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"resolvers":[]`) {
		t.Fatal(response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	a.controlHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/network", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"revision":"sha256:`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestNetworkPolicyIsSeparateFromFixedReleaseIdentity(t *testing.T) {
	cfg, _ := releaseConfiguration(t)
	before, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Project.Reachability.Resolvers = []netip.Addr{netip.MustParseAddr("192.0.2.53")}
	after, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if before.CompatibilityDigest != after.CompatibilityDigest || before.ConfigurationDigest != after.ConfigurationDigest || before.NetworkPolicyRevision == after.NetworkPolicyRevision {
		t.Fatal(before, after)
	}
	cfg.Limits.Agents++
	fixed, err := InspectRelease(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.CompatibilityDigest == after.CompatibilityDigest {
		t.Fatal("fixed identity weakened")
	}
}
