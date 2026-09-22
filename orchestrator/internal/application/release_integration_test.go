//go:build khoraintegration

package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type releaseApp struct {
	latencies                    []time.Duration
	t                            *testing.T
	ctx                          context.Context
	root, binary, config, origin string
	command                      *exec.Cmd
	client                       *http.Client
	uid, gid                     uint32
}

func (a *releaseApp) start() {
	a.t.Helper()
	log, err := os.OpenFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "application.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	require(a.t, err)
	defer log.Close()
	a.command = exec.CommandContext(a.ctx, a.binary, "serve", "--config", a.config)
	a.command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: a.uid, Gid: a.gid}}
	a.command.Env = append(os.Environ(), "PROJECT_RELEASE_SECRET=release-fixture-secret")
	a.command.Stdout, a.command.Stderr = log, log
	require(a.t, a.command.Start())
	a.await(30*time.Second, "application ready", func() bool {
		response, err := (&http.Client{Timeout: time.Second}).Get(a.origin + "/api/health")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	})
}
func (a *releaseApp) stop() {
	if a.command != nil {
		_ = a.command.Process.Kill()
		_ = a.command.Wait()
		a.command = nil
	}
}
func (a *releaseApp) await(limit time.Duration, name string, check func() bool) {
	a.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.t.Fatalf("timed out: %s (inspect application.log)", name)
}
func (a *releaseApp) request(method, path string, body any, want int) map[string]any {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require(a.t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(a.ctx, method, a.origin+path, reader)
	require(a.t, err)
	req.Header.Set("Origin", a.origin)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	response, err := a.client.Do(req)
	require(a.t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	a.latencies = append(a.latencies, time.Since(start))
	require(a.t, err)
	if (want != 0 && response.StatusCode != want) || (want == 0 && response.StatusCode/100 != 2) {
		a.t.Fatalf("%s %s: %d wanted %d: %s", method, path, response.StatusCode, want, raw)
	}
	var value map[string]any
	if len(raw) > 0 {
		require(a.t, json.Unmarshal(raw, &value))
	}
	return value
}
func (a *releaseApp) cli(args ...string) map[string]any {
	a.t.Helper()
	cmd := exec.CommandContext(a.ctx, a.binary, args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		a.t.Fatalf("operator %v: %v: %s", args, err, raw)
	}
	var value map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		require(a.t, json.Unmarshal(raw, &value))
	}
	return value
}
func newReleasedApp(t *testing.T) *releaseApp {
	t.Helper()
	endpoint, binaries, manifestPath := os.Getenv("PROJECT_TEST_ENDPOINT"), os.Getenv("PROJECT_TEST_BIN_DIR"), os.Getenv("PROJECT_TEST_MANIFEST")
	if endpoint == "" || binaries == "" || manifestPath == "" {
		t.Fatal("isolated release harness required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	t.Cleanup(cancel)
	api, err := sdk.New(endpoint)
	require(t, err)
	deadline := time.Now().Add(15 * time.Second)
	for api.Ready(ctx) != nil {
		if time.Now().After(deadline) {
			t.Fatal("engine not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(ctx, khora.Config{Endpoint: endpoint})
	require(t, err)
	identity, err := backend.Identity(ctx)
	require(t, err)
	root, err := os.MkdirTemp("/run", "project-release-")
	require(t, err)
	t.Cleanup(func() { os.RemoveAll(root) })
	uid, err := strconv.Atoi(os.Getenv("PROJECT_TEST_VIA_UID"))
	require(t, err)
	gid, err := strconv.Atoi(os.Getenv("PROJECT_TEST_VIA_GID"))
	require(t, err)
	require(t, os.Chmod(root, 0755))
	for _, name := range []string{"state", "content", "content/pi", "assets"} {
		require(t, os.MkdirAll(filepath.Join(root, name), 0755))
		require(t, os.Chown(filepath.Join(root, name), uid, gid))
	}
	for _, name := range []string{"orchestrator", "runtime-fixture"} {
		raw, err := os.ReadFile(filepath.Join(binaries, name))
		require(t, err)
		require(t, os.WriteFile(filepath.Join(root, name), raw, 0755))
	}
	raw, err := os.ReadFile(manifestPath)
	require(t, err)
	digest, err := runtimerelease.Digest(raw)
	require(t, err)
	require(t, os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0644))
	hash, err := auth.HashPasswordWithIterations("isolated-browser-fixture", 1000)
	require(t, err)
	require(t, os.WriteFile(filepath.Join(root, "users.yaml"), []byte("users:\n  - username: researcher\n    password_hash: "+hash+"\n    role: admin\n"), 0644))
	models := `{"providers":{"gateway":{"baseUrl":"http://model.internal:443","api":"anthropic-messages","apiKey":"project-service-token","models":[{"id":"local-model","name":"Local model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`
	require(t, os.WriteFile(filepath.Join(root, "content/pi/models.json"), []byte(models), 0644))
	require(t, os.WriteFile(filepath.Join(root, "content/pi/settings.json"), []byte(`{"defaultProvider":"gateway","defaultModel":"local-model","compaction":{"enabled":false}}`), 0644))
	require(t, os.WriteFile(filepath.Join(root, "assets/AGENTS.md"), []byte("Reviewed release qualification context."), 0644))
	if os.Getenv("PROJECT_RELEASE_CHECK_CONTEXT") == "1" {
		require(t, os.MkdirAll(filepath.Join(root, "assets/skills/qualification"), 0755))
		require(t, os.WriteFile(filepath.Join(root, "assets/skills/qualification/SKILL.md"), []byte("---\nname: qualification\ndescription: Reviewed qualification skill\n---\nUse only the declared Project workspace.\n"), 0644))
	}

	mode := os.Getenv("PROJECT_RELEASE_MODEL_MODE")
	if mode == "" {
		mode = "upstream"
	}
	upstream := exec.CommandContext(ctx, filepath.Join(root, "runtime-fixture"), mode)
	upstream.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	upstream.Env = append(os.Environ(), "PROJECT_UPSTREAM_ADDR=192.0.2.1:18890", "PROJECT_UPSTREAM_SECRET=release-fixture-secret", "PROJECT_UPSTREAM_OVERLAP=4", "PROJECT_MODEL_ADDR=192.0.2.1:18890", "PROJECT_UPSTREAM_COMPLETIONS="+filepath.Join(root, "completions.jsonl"), "PROJECT_UPSTREAM_REQUIRE_CONTEXT="+os.Getenv("PROJECT_RELEASE_CHECK_CONTEXT"))
	upstream.Stdout, upstream.Stderr = os.Stdout, os.Stderr
	require(t, os.WriteFile(filepath.Join(root, "completions.jsonl"), nil, 0666))
	require(t, os.Chown(filepath.Join(root, "completions.jsonl"), uid, gid))
	require(t, upstream.Start())
	t.Cleanup(func() { _ = upstream.Process.Kill(); _ = upstream.Wait() })
	engine := engineTLS(t, root, endpoint)
	engine["store_id"] = identity.StoreID
	cfg := map[string]any{
		"version": 1, "listen": "127.0.0.1:18192", "frontend": "http://192.0.2.1:18193", "state_directory": "state",
		"engine":   engine,
		"template": map[string]any{"id": "battery", "manifest": "manifest.json", "digest": digest},
		"auth":     map[string]any{"users_file": "users.yaml"}, "content": map[string]any{"agent_config": "content", "shared_assets": "assets"},
		"project":  map[string]any{"cpu_millis": 2000, "memory_bytes": 2 << 30, "disk_bytes": 256 << 20, "workspace_bytes": 128 << 20, "runtime_bytes": 128 << 20, "reachability": map[string]any{"services": []any{map[string]any{"audience": "model", "destination": "10.80.0.1", "port": 443, "gateway": "192.0.2.1:18891"}}, "hosts": map[string]string{"model.internal": "model"}}},
		"gateways": []any{map[string]any{"audience": "model", "listen": "192.0.2.1:18891", "upstream": "http://192.0.2.1:18890", "api_key_env": "PROJECT_RELEASE_SECRET", "max_requests": 8}}, "agent_audience": "model",
		"limits": map[string]any{"idle_after": "10s", "idle_agent_after": "5s", "interval": "100ms"},
	}
	raw, err = json.Marshal(cfg)
	require(t, err)
	require(t, os.WriteFile(filepath.Join(root, "application.json"), raw, 0644))
	jar, err := cookiejar.New(nil)
	require(t, err)
	a := &releaseApp{t: t, ctx: ctx, root: root, binary: filepath.Join(root, "orchestrator"), config: filepath.Join(root, "application.json"), origin: "http://127.0.0.1:18192", uid: uint32(uid), gid: uint32(gid), client: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	t.Cleanup(a.stop)
	a.start()
	a.request("POST", "/api/auth/login", map[string]string{"username": "researcher", "password": "isolated-browser-fixture"}, 200)
	return a
}

func TestReleasedExecutableOnKhora(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_CHECK_CONTEXT", "1")
	a := newReleasedApp(t)
	socket := filepath.Join(a.root, "state/control.sock")
	status := a.cli("diagnostics", "--socket", socket)
	if status["runtime_digest"] == "" {
		t.Fatal("release identity absent")
	}
	a.cli("publish", "--socket", socket)
	var paths []string
	for p := 0; p < 2; p++ {
		id := fmt.Sprintf("release-%d", p)
		a.request("POST", "/api/projects", map[string]string{"id": id, "title": id}, 201)
		for s := 0; s < 2; s++ {
			session := fmt.Sprintf("agent-%d-%d", p, s)
			base := "/api/projects/" + id + "/sessions/" + session
			a.request("POST", "/api/projects/"+id+"/sessions", map[string]string{"id": session, "title": session}, 201)
			a.request("POST", base+"/turns", map[string]string{"request_id": "initial", "content": "qualify released native runtime"}, 201)
			paths = append(paths, base)
		}
	}
	for _, base := range paths {
		a.await(45*time.Second, "four concurrent native turns", func() bool {
			v := a.request("GET", base+"/turns", nil, 200)
			turns, _ := v["turns"].([]any)
			return len(turns) == 1 && turns[0].(map[string]any)["state"] == "completed"
		})
		a.await(10*time.Second, "display catches native completion", func() bool { return a.request("GET", base+"/view/head", nil, 0)["projection_state"] == "ready" })
		v := a.request("GET", base+"/view/pages", nil, 200)
		raw, _ := json.Marshal(v)
		if !bytes.Contains(raw, []byte("deterministic-local-reply")) {
			t.Fatalf("native reply missing: %s", raw)
		}
	}
	for p := 0; p < 2; p++ {
		report := a.cli("diagnostics", "--socket", socket, "--project", fmt.Sprintf("release-%d", p))
		if report["configuration_matches"] != true {
			t.Fatalf("published mounts or network differ: %v", report)
		}
	}
	// Admission closure is durable across a real owner crash. It is not yet an
	// activation/readiness claim: this gate deliberately leaves gateways coupled.
	a.await(5*time.Second, "runtime operation leases settle", func() bool {
		report := a.cli("drain-status", "--socket", socket)
		work, ok := report["runtime_work"].(map[string]any)
		return ok && len(work) == 0
	})
	release := a.cli("drain", "--socket", socket, "--owner", "release-fixture", "--timeout-seconds", "60")
	if release["phase"] != "draining" {
		t.Fatal(release)
	}
	a.request("POST", paths[0]+"/turns", map[string]string{"request_id": "during-drain", "content": "blocked until abort"}, 503)
	a.request("POST", paths[0]+"/turns", map[string]any{"request_id": "held-during-drain", "content": "held intent", "held": true}, 201)
	// A real owner crash retains the credential session and reconciles durable state.
	start := time.Now()
	a.stop()
	a.start()
	retained := a.cli("drain-status", "--socket", socket)
	if retained["phase"] != "draining" || retained["generation"] != release["generation"] {
		t.Fatal("release fence lost across restart", release, retained)
	}
	a.request("POST", paths[0]+"/turns", map[string]string{"request_id": "during-drain", "content": "blocked until abort"}, 503)
	a.cli("drain-abort", "--socket", socket, "--owner", "release-fixture", "--generation", fmt.Sprint(release["generation"]))
	a.request("GET", "/api/projects", nil, 200)
	if time.Since(start) > 15*time.Second {
		t.Fatal("owner restart exceeded target")
	}
	for _, base := range paths {
		a.request("POST", base+"/turns", map[string]string{"request_id": "after-restart", "content": "continue after owner restart"}, 201)
	}
	for _, base := range paths {
		a.await(30*time.Second, "post-restart native continuation", func() bool {
			v := a.request("GET", base+"/turns", nil, 200)
			turns, _ := v["turns"].([]any)
			completed := false
			for _, raw := range turns {
				turn := raw.(map[string]any)
				if turn["request_id"] == "held-during-drain" && turn["state"] != "held" {
					t.Fatal("held intent dispatched", turn)
				}
				if turn["request_id"] == "after-restart" && turn["state"] == "completed" {
					completed = true
				}
			}
			return completed
		})
	}
	// Saved owner scope is required even when the caller owns both Projects.
	a.request("GET", "/api/projects/release-1/sessions/agent-0-0", nil, 404)
	for p := 0; p < 2; p++ {
		base := fmt.Sprintf("/api/projects/release-%d", p)
		a.await(20*time.Second, "idle Project pause", func() bool { return a.request("GET", base, nil, 200)["state"] == "paused" })
	}
	start = time.Now()
	for p := 0; p < 2; p++ {
		base := fmt.Sprintf("/api/projects/release-%d", p)
		v := a.request("GET", base, nil, 200)
		a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", base, v["revision"]), nil, 202)
	}
	a.await(15*time.Second, "owned resource cleanup", func() bool {
		v := a.cli("diagnostics", "--socket", socket)
		state := v["state"].(map[string]any)
		counts := state["projects"].(map[string]any)
		return counts["deleted"] == float64(2) && state["occupied_projects"] == float64(0)
	})
	t.Logf("release runtime qualified; cleanup=%s", time.Since(start))
	// The executable exposes no retired mode or alternate datastore path.
	cmd := exec.CommandContext(a.ctx, a.binary, "--help")
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "session-db") {
		t.Fatal("legacy CLI survived")
	}
}
