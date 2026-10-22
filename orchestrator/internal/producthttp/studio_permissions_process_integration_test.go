//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func provisionStudioPermissionsBrowser(t *testing.T, ctx context.Context, root, origin, address, token, account string, handler *http.Handler) {
	t.Helper()
	command := exec.CommandContext(ctx, "node", filepath.Join(root, "web/serve-standalone.mjs"), "admin-frontend", strings.Split(address, ":")[1])
	command.Stdout = io.Discard
	command.Stderr = os.Stderr
	must(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	})
	endpoint, _ := url.Parse("http://" + address)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { host := r.In.Host; r.SetURL(endpoint); r.Out.Host = host }}
	original := *handler
	*handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
			proxy.ServeHTTP(w, r)
			return
		}
		original.ServeHTTP(w, r)
	})
	ready := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		response, err := http.Get(endpoint.String() + "/admin")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("admin frontend unavailable")
	}
	width := 1440
	if os.Getenv("STUDIO_NARROW") == "1" {
		width = 390
	}
	config, _ := json.Marshal(map[string]any{"origin": origin, "token": token, "account": account, "width": width, "browser": os.Getenv("STUDIO_TEST_BROWSER"), "evidence_directory": filepath.Join(root, "admin-frontend/test-results/studio-permissions")})
	browser := exec.CommandContext(ctx, "node", filepath.Join(root, "volumes-studio/tests/browser/permissions.mjs"))
	browser.Stdin = bytes.NewReader(config)
	output, err := browser.CombinedOutput()
	if err != nil {
		t.Fatalf("Studio permissions browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}
