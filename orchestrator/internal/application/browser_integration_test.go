//go:build khoraintegration

package application_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Use the packaged frontend during candidate qualification. Development runs
// retain the source build, while Playwright always comes from the test checkout.
func releasedFrontend(ctx context.Context, repo string) *exec.Cmd {
	if candidate := os.Getenv("PROJECT_TEST_CANDIDATE"); candidate != "" {
		command := exec.CommandContext(ctx, "node", "server.js")
		command.Dir = filepath.Join(candidate, "frontend")
		command.Env = append(os.Environ(), "HOSTNAME=192.0.2.1", "PORT=18193", "NODE_ENV=production")
		return command
	}
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "frontend/node_modules/next/dist/bin/next"), "start", "--hostname", "192.0.2.1", "--port", "18193")
	command.Dir = filepath.Join(repo, "frontend")
	return command
}

func TestReleasedBrowser(t *testing.T) {
	// The coordinated native fixture responds only after two model requests and
	// supports streaming interruption. It lives outside the released guest image.
	runReleasedBrowser(t, "model", "--grep-invert", "held queue prompt Steers into the active native run")
}

func TestReleasedQueueBrowser(t *testing.T) {
	runReleasedBrowser(t, "model-independent", "--grep", "held queue prompt Steers into the active native run")
}

func runReleasedBrowser(t *testing.T, mode string, selection ...string) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", mode)
	a := newReleasedApp(t)
	repo := os.Getenv("PROJECT_TEST_REPO")
	if repo == "" {
		t.Fatal("PROJECT_TEST_REPO required")
	}
	log, err := os.Create(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "frontend.log"))
	require(t, err)
	defer log.Close()
	frontend := releasedFrontend(a.ctx, repo)
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
	args := append([]string{filepath.Join(repo, "frontend/node_modules/@playwright/test/cli.js"), "test", "--config", "playwright.config.ts"}, selection...)
	browser := exec.CommandContext(a.ctx, "node", args...)
	browser.Dir = filepath.Join(repo, "frontend")
	browser.Env = append(os.Environ(), "PROJECT_BROWSER_URL="+a.origin, "PROJECT_BROWSER_SCREENSHOT="+filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "workbench.png"))
	output, err := browser.CombinedOutput()
	t.Log(string(output))
	require(t, err)
}
