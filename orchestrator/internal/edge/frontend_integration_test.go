//go:build webintegration

package edge_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/edge"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/producthttp"
	"harness-platform/orchestrator/internal/testfixture"
)

// This uses both production standalone builds, the real Go edge and database
// login, without an engine. It does not qualify sandbox or lifecycle behavior.
func TestProductionFrontendBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	start := func(name, route string) *url.URL {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		_, port, _ := net.SplitHostPort(address)
		log, err := os.Create(filepath.Join(t.TempDir(), name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		command := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/serve-standalone.mjs"), name, port)
		command.Stdout = log
		command.Stderr = log
		if err = command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
		origin := "http://" + address
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			response, err := client.Get(origin + route)
			if err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == 200 {
					parsed, _ := url.Parse(origin)
					return parsed
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		raw, _ := os.ReadFile(log.Name())
		t.Fatalf("%s did not start: %s", name, raw)
		return nil
	}
	workbench, admin := start("frontend", "/"), start("admin-frontend", "/admin")
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manifest, digest := testfixture.RuntimeManifest()
	network, err := product.NewNetworkPolicy(product.Reachability{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := product.NewBootstrapSeed(product.ResourceSeed{CPU: 1000, Memory: 1 << 30, Overlay: 256 << 20, Workspace: 1 << 30, Runtime: 1 << 29, MaxMounts: 8, ProjectSlots: 4, AgentSlots: 8, TerminalSlots: 8}, product.RegisteredSandbox{Reference: product.ArtifactReference{ID: "web-test", Version: "v1", Digest: digest}, StoreID: "web-engine", TemplateID: "web-test", Manifest: manifest}, nil, network.Resolvers)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.BootstrapAccount(ctx, "administrator", "web-test-permanent-password", seed)
	if err != nil {
		t.Fatal(err)
	}
	authentication := producthttp.NewAuthentication(store, producthttp.Config{TrustForwardedHost: true})
	app := httptest.NewServer(authentication.Handler((&producthttp.Server{Store: store, Auth: authentication, Shares: &product.Shares{Store: store, Directory: t.TempDir()}}).Handler()))
	defer app.Close()
	appURL, _ := url.Parse(app.URL)
	handler, err := edge.New(edge.Config{Application: appURL, Frontend: workbench, AdminFrontend: admin, PageAccount: authentication.Actor})
	if err != nil {
		t.Fatal(err)
	}
	browser := httptest.NewServer(handler)
	defer browser.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	request, err := http.NewRequestWithContext(ctx, "POST", browser.URL+"/api/auth/login", strings.NewReader(`{"username":"administrator","password":"web-test-permanent-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", browser.URL)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("real login failed", response.StatusCode)
	}
	scripts := regexp.MustCompile(`src="([^" ]+\.js(?:\?[^" ]*)?)"`)
	for _, route := range []string{"/", "/admin", "/admin/users/example-account", "/admin/audit"} {
		response, err := client.Get(browser.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatal(route, response.StatusCode, err)
		}
		html := string(raw)
		if !strings.Contains(html, `data-account-id="`+account.ID+`"`) {
			t.Fatal("document lost verified identity", route)
		}
		want := "/_next/static/"
		if route != "/" {
			want = "/admin/_next/static/"
			if !strings.Contains(html, "administration") {
				t.Fatal("admin document came from workbench", route)
			}
		}
		count := 0
		for _, match := range scripts.FindAllStringSubmatch(html, -1) {
			asset := match[1]
			if !strings.Contains(asset, "/_next/static/") {
				continue
			}
			count++
			if !strings.HasPrefix(asset, want) {
				t.Fatal("asset escaped frontend base path", route, asset)
			}
			assetResponse, err := client.Get(browser.URL + asset)
			if err != nil {
				t.Fatal(err)
			}
			size, err := io.Copy(io.Discard, assetResponse.Body)
			assetResponse.Body.Close()
			if err != nil || assetResponse.StatusCode != 200 || size == 0 {
				t.Fatal("production asset unavailable", route, asset, err)
			}
		}
		if count == 0 {
			t.Fatal("document has no production scripts", route)
		}
	}
	response, err = client.Get(browser.URL + "/administrator")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 404 || strings.Contains(string(raw), "/admin/_next/static/") {
		t.Fatal("administrator matched admin routing")
	}

	browserTest := exec.CommandContext(ctx, "node", filepath.Join(repo, "admin-frontend/node_modules/@playwright/test/cli.js"), "test", "--config", "playwright.live.config.ts")
	browserTest.Dir = filepath.Join(repo, "admin-frontend")
	browserTest.Env = append(os.Environ(), "ADMIN_LIVE_URL="+browser.URL)
	output, err := browserTest.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("real administration browser journey failed", err)
	}
}
