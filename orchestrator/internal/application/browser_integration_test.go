//go:build khoraintegration

package application_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleasedBrowser(t *testing.T) {
	// The coordinated native fixture responds only after two model requests and
	// supports streaming interruption. It lives outside the released guest image.
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model")
	a := newReleasedAppBinary(t, os.Getenv("PROJECT_BROWSER_OWNER_BINARY"))
	repo := os.Getenv("PROJECT_TEST_REPO")
	if repo == "" {
		t.Fatal("PROJECT_TEST_REPO required")
	}
	log, err := os.Create(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "frontend.log"))
	require(t, err)
	defer log.Close()
	frontend := exec.CommandContext(a.ctx, "node", filepath.Join(repo, "frontend/node_modules/next/dist/bin/next"), "start", "--hostname", "192.0.2.1", "--port", "18193")
	frontend.Dir = filepath.Join(repo, "frontend")
	frontend.Stdout, frontend.Stderr = log, log
	require(t, frontend.Start())
	t.Cleanup(func() { _ = frontend.Process.Kill(); _ = frontend.Wait() })
	a.await(15*time.Second, "frontend production assets", func() bool {
		r, e := (&http.Client{Timeout: time.Second}).Get(a.origin + "/")
		if e != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	})
	browser := exec.CommandContext(a.ctx, "node", filepath.Join(repo, "frontend/node_modules/@playwright/test/cli.js"), "test", "--config", "playwright.config.ts")
	browser.Dir = filepath.Join(repo, "frontend")
	browser.Env = append(os.Environ(), "PROJECT_BROWSER_URL="+a.origin, "PROJECT_BROWSER_SCREENSHOT="+filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "workbench.png"))
	output, err := browser.CombinedOutput()
	t.Log(string(output))
	require(t, err)
}
