//go:build khoraintegration

package producthttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/edge"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

// browserSubstrate installs only deterministic test dependencies into disposable
// guest placements. The application, auth, workers and proxy are production code.
type browserSubstrate struct {
	*khora.Backend
	api               *sdk.Client
	binaries          string
	mu                sync.Mutex
	initialized       map[string]bool
	modelMode         string
	loseDeleteReplies bool
	lostDestroy       map[string]bool
	lostVolumes       map[string]bool
	destroyed         chan struct{}
}

func (b *browserSubstrate) WriteProjection(ctx context.Context, ref sandbox.Reference, path string, data []byte) error {
	if err := b.Backend.WriteProjection(ctx, ref, path, data); err != nil {
		return err
	}
	if path != "/etc/resolv.conf" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := fmt.Sprintf("%s/%d", ref.SandboxID, ref.Epoch)
	if b.initialized[key] {
		return nil
	}
	for _, name := range []string{"agent-runner", "workspace-files", "runtime-fixture", "runtime-cleanup"} {
		f, err := os.Open(filepath.Join(b.binaries, name))
		if err != nil {
			return err
		}
		err = b.api.WriteFile(ctx, ref.SandboxID, "/usr/local/bin/"+name, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	handle := fmt.Sprintf("browser-setup-%s-%d", ref.SandboxID, ref.Epoch)
	stream, err := b.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: ref.StoreID, SandboxID: ref.SandboxID, Epoch: ref.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", "chmod +x /usr/local/bin/agent-runner /usr/local/bin/workspace-files /usr/local/bin/runtime-fixture /usr/local/bin/runtime-cleanup; mkdir -p /opt/agent-config/pi"}, Directory: "/workspace"})
	if err != nil {
		return err
	}
	for {
		f, err := stream.Next()
		if err != nil {
			stream.Close()
			return err
		}
		if f.Kind == "end" {
			stream.Close()
			if f.ExitCode != 0 {
				return fmt.Errorf("setup exit %d", f.ExitCode)
			}
			break
		}
	}
	models := `{"providers":{"local":{"baseUrl":"http://127.0.0.1:18889","api":"anthropic-messages","apiKey":"fixture-only","models":[{"id":"local-model","name":"Local model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`
	if err = b.api.WriteFile(ctx, ref.SandboxID, "/opt/agent-config/pi/models.json", strings.NewReader(models)); err != nil {
		return err
	}
	if err = b.api.WriteFile(ctx, ref.SandboxID, "/opt/agent-config/pi/settings.json", strings.NewReader(`{"defaultProvider":"local","defaultModel":"local-model","compaction":{"enabled":false}}`)); err != nil {
		return err
	}
	mode := b.modelMode
	if mode == "" {
		mode = "model"
	}
	model, err := b.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: ref.StoreID, SandboxID: ref.SandboxID, Epoch: ref.Epoch, Handle: fmt.Sprintf("browser-model-%s-%d", ref.SandboxID, ref.Epoch)}, IdempotencyKey: fmt.Sprintf("browser-model-%s-%d", ref.SandboxID, ref.Epoch), Command: "/usr/local/bin/runtime-fixture", Arguments: []string{mode}, Directory: "/workspace"})
	if err != nil {
		return err
	}
	// Keep the model process alive after fixture setup; its stream can detach.
	for {
		f, err := model.Next()
		if err != nil {
			model.Close()
			return err
		}
		if strings.Contains(string(f.Data), "model-ready") {
			model.Close()
			break
		}
		if f.Kind == "end" {
			model.Close()
			return fmt.Errorf("model exited")
		}
	}
	b.initialized[key] = true
	return nil
}
func TestProjectBrowserOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	repo := os.Getenv("PROJECT_TEST_REPO")
	if endpoint == "" || repo == "" {
		t.Fatal("isolated endpoint and repository required")
	}
	runctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	api, err := sdk.New(endpoint)
	must(t, err)
	for i := 0; i < 100; i++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	fixture := &browserSubstrate{Backend: backend, api: api, binaries: os.Getenv("PROJECT_TEST_BIN_DIR"), initialized: map[string]bool{}}
	store, err := product.Open(runctx, filepath.Join(t.TempDir(), "browser.db"))
	must(t, err)
	defer store.Close()
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "battery"
	profile.DiskBytes = 256 << 20
	lifecycle := &product.Lifecycle{Store: store, Backend: fixture, MaxProjects: 4}
	agents := product.NewAgents(runctx, store, lifecycle, backend, profile, 8)
	defer agents.Close()
	terminals := product.NewTerminals(runctx, store, lifecycle, backend, profile, 8)
	defer terminals.Close()
	files := &product.Files{Store: store, Lifecycle: lifecycle, Backend: backend, Profile: profile}
	shares := &product.Shares{Store: store, Files: files, Directory: t.TempDir()}
	captures := &product.Captures{Store: store, Backend: backend, Directory: t.TempDir()}
	lifecycle.Artifacts = &product.Artifacts{Store: store, Captures: captures, Shares: shares}
	recovery := &product.AgentRecovery{Store: store, Lifecycle: lifecycle, Backend: backend}
	retirement := &product.ChildRetirement{Store: store, Lifecycle: lifecycle, Backend: backend, Captures: captures, Shares: shares}
	workers := &product.Workers{Store: store, Lifecycle: lifecycle, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Captures: captures, Recovery: recovery, Retirement: retirement, IdleAfter: 5 * time.Minute, Interval: 100 * time.Millisecond, Report: func(kind string, err error) { t.Logf("worker %s: %v", kind, err) }}
	workerctx, stopWorkers := context.WithCancel(runctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); workers.Run(workerctx) }()
	defer func() { stopWorkers(); <-workerDone }()
	hash, err := auth.HashPasswordWithIterations("isolated-browser-fixture", 1000)
	must(t, err)
	authentication, err := NewAuthentication(store, Config{TrustForwardedHost: true}, []AuthUser{{Username: "researcher", PasswordHash: hash}}, "")
	must(t, err)
	app := httptest.NewServer(authentication.Handler((&Server{Store: store, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Lifecycle: lifecycle, Profile: profile, Auth: authentication, Config: Config{TrustForwardedHost: true}}).Handler()))
	defer app.Close()
	log, err := os.Create(filepath.Join(os.Getenv("PROJECT_TEST_BIN_DIR"), "S08-frontend.log"))
	must(t, err)
	defer log.Close()
	frontend := exec.CommandContext(runctx, "node", filepath.Join(repo, "frontend/node_modules/next/dist/bin/next"), "start", "--hostname", "127.0.0.1", "--port", "18193")
	frontend.Dir = filepath.Join(repo, "frontend")
	frontend.Stdout = log
	frontend.Stderr = log
	must(t, frontend.Start())
	defer func() { _ = frontend.Process.Kill(); _ = frontend.Wait() }()
	frontendURL, _ := url.Parse("http://127.0.0.1:18193")
	appURL, _ := url.Parse(app.URL)
	handler, err := edge.New(edge.Config{Application: appURL, Frontend: frontendURL})
	must(t, err)
	edgeServer := httptest.NewServer(handler)
	defer edgeServer.Close()
	ready := false
	for i := 0; i < 100; i++ {
		response, err := http.Get(frontendURL.String())
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("frontend did not start")
	}
	browser := exec.CommandContext(runctx, "node", filepath.Join(repo, "frontend/node_modules/@playwright/test/cli.js"), "test", "--config", "playwright.config.ts")
	browser.Dir = filepath.Join(repo, "frontend")
	browser.Env = append(os.Environ(), "PROJECT_BROWSER_URL="+edgeServer.URL, "PROJECT_BROWSER_SCREENSHOT="+filepath.Join(os.Getenv("PROJECT_TEST_BIN_DIR"), "S08-workbench.png"))
	output, err := browser.CombinedOutput()
	t.Log(string(output))
	must(t, err)
}

func (b *browserSubstrate) Destroy(ctx context.Context, ref sandbox.Reference) error {
	if err := b.Backend.Destroy(ctx, ref); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loseDeleteReplies {
		if b.lostDestroy == nil {
			b.lostDestroy = map[string]bool{}
		}
		if !b.lostDestroy[ref.SandboxID] {
			b.lostDestroy[ref.SandboxID] = true
			if b.destroyed != nil {
				select {
				case b.destroyed <- struct{}{}:
				default:
				}
			}
			return errors.New("injected lost successful sandbox deletion reply")
		}
	}
	return nil
}
func (b *browserSubstrate) DeleteVolume(ctx context.Context, ref sandbox.VolumeReference) error {
	if err := b.Backend.DeleteVolume(ctx, ref); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loseDeleteReplies {
		if b.lostVolumes == nil {
			b.lostVolumes = map[string]bool{}
		}
		if !b.lostVolumes[ref.VolumeID] {
			b.lostVolumes[ref.VolumeID] = true
			return errors.New("injected lost successful volume deletion reply")
		}
	}
	return nil
}
