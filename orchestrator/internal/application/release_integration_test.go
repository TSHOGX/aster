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
	gateway                      *exec.Cmd
	client                       *http.Client
	uid, gid                     uint32
}

func (a *releaseApp) start(extra ...string) {
	a.t.Helper()
	log, err := os.OpenFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "application.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	require(a.t, err)
	defer log.Close()
	a.command = exec.CommandContext(a.ctx, a.binary, append([]string{"serve", "--config", a.config}, extra...)...)
	a.command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: a.uid, Gid: a.gid}}
	a.command.Env = os.Environ()
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
func (a *releaseApp) startGateway() {
	a.t.Helper()
	log, err := os.OpenFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "gateway.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	require(a.t, err)
	defer log.Close()
	a.gateway = exec.CommandContext(a.ctx, a.binary, "gateway", "--config", a.config)
	a.gateway.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: a.uid, Gid: a.gid}}
	a.gateway.Env = append(os.Environ(), "PROJECT_RELEASE_SECRET=release-fixture-secret")
	a.gateway.Stdout, a.gateway.Stderr = log, log
	require(a.t, a.gateway.Start())
	a.await(10*time.Second, "independent gateway ready", func() bool {
		command := exec.CommandContext(a.ctx, a.binary, "gateway-status", "--socket", filepath.Join(a.root, "state/gateway.sock"))
		_, err := command.Output()
		return err == nil
	})
}
func (a *releaseApp) stopGateway() {
	if a.gateway != nil {
		_ = a.gateway.Process.Kill()
		_ = a.gateway.Wait()
		a.gateway = nil
	}
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
	for _, name := range []string{"orchestrator", "orchestrator-candidate", "runtime-fixture"} {
		source := filepath.Join(binaries, name)
		raw, err := os.ReadFile(source)
		require(t, err)
		require(t, os.WriteFile(filepath.Join(root, name), raw, 0755))
	}
	raw, err := os.ReadFile(manifestPath)
	require(t, err)
	digest, err := runtimerelease.Digest(raw)
	require(t, err)
	require(t, os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0644))
	models := `{"providers":{"gateway":{"baseUrl":"http://model.internal:443","api":"anthropic-messages","apiKey":"project-service-token","models":[{"id":"local-model","name":"Local model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`
	if os.Getenv("PROJECT_PROVIDER_SOCKET") != "" {
		raw, _ := json.Marshal(map[string]any{"providers": map[string]any{"gateway": map[string]any{"baseUrl": "http://model.internal:443/v1", "api": "openai-completions", "apiKey": "project-service-token", "models": []any{map[string]any{"id": os.Getenv("PROJECT_PROVIDER_MODEL"), "name": "Qualified provider", "reasoning": false, "input": []string{"text"}, "contextWindow": 32768, "maxTokens": 4096, "compat": map[string]any{"supportsDeveloperRole": false, "maxTokensField": "max_tokens"}, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}}}}})
		models = string(raw)
	}
	require(t, os.WriteFile(filepath.Join(root, "content/pi/models.json"), []byte(models), 0644))
	settings := `{"defaultProvider":"gateway","defaultModel":"local-model","compaction":{"enabled":false}}`
	if os.Getenv("PROJECT_RELEASE_RETRY_TEST") == "1" {
		settings = `{"defaultProvider":"gateway","defaultModel":"local-model","compaction":{"enabled":false},"retry":{"enabled":true,"maxRetries":2,"baseDelayMs":3000}}`
	}
	if os.Getenv("PROJECT_RELEASE_COMPACTION_TEST") == "1" {
		settings = `{"defaultProvider":"gateway","defaultModel":"local-model","compaction":{"enabled":true,"reserveTokens":256,"keepRecentTokens":128}}`
	}
	if os.Getenv("PROJECT_PROVIDER_SOCKET") != "" {
		raw, _ := json.Marshal(map[string]any{"defaultProvider": "gateway", "defaultModel": os.Getenv("PROJECT_PROVIDER_MODEL"), "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}})
		settings = string(raw)
	}
	require(t, os.WriteFile(filepath.Join(root, "content/pi/settings.json"), []byte(settings), 0644))
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
	if mode != "provider-bridge" {
		upstream.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	}
	upstream.Env = append(os.Environ(), "PROJECT_UPSTREAM_ADDR=192.0.2.1:18890", "PROJECT_UPSTREAM_SECRET=release-fixture-secret", "PROJECT_UPSTREAM_OVERLAP=4", "PROJECT_MODEL_ADDR=192.0.2.1:18890", "PROJECT_UPSTREAM_COMPLETIONS="+filepath.Join(root, "completions.jsonl"), "PROJECT_UPSTREAM_REPORT="+filepath.Join(root, "requests.jsonl"), "PROJECT_UPSTREAM_REQUIRE_CONTEXT="+os.Getenv("PROJECT_RELEASE_CHECK_CONTEXT"))
	upstream.Stdout, upstream.Stderr = os.Stdout, os.Stderr
	require(t, os.WriteFile(filepath.Join(root, "completions.jsonl"), nil, 0666))
	require(t, os.Chown(filepath.Join(root, "completions.jsonl"), uid, gid))
	require(t, os.WriteFile(filepath.Join(root, "requests.jsonl"), nil, 0666))
	require(t, os.Chown(filepath.Join(root, "requests.jsonl"), uid, gid))
	require(t, upstream.Start())
	t.Cleanup(func() { _ = upstream.Process.Kill(); _ = upstream.Wait() })
	engine := engineTLS(t, root, endpoint)
	engine["store_id"] = identity.StoreID
	cfg := map[string]any{
		"version": 1, "listen": "127.0.0.1:18192", "frontend": "http://192.0.2.1:18193", "admin_frontend": "http://192.0.2.1:18194", "state_directory": "state",
		"engine":   engine,
		"template": map[string]any{"version": "v1", "id": "agent-runtime", "manifest": "manifest.json", "digest": digest},
		"content":  map[string]any{"agent_config": "content", "shared_assets": "assets"},
		"project":  map[string]any{"cpu_millis": 2000, "memory_bytes": 2 << 30, "disk_bytes": 256 << 20, "workspace_bytes": 128 << 20, "runtime_bytes": 128 << 20, "reachability": map[string]any{"services": []any{map[string]any{"audience": "model", "destination": "10.80.0.1", "port": 443, "gateway": "192.0.2.1:18891"}}, "hosts": map[string]string{"model.internal": "model"}}},
		"gateways": []any{map[string]any{"audience": "model", "listen": "192.0.2.1:18891", "upstream": "http://192.0.2.1:18890", "api_key_env": "PROJECT_RELEASE_SECRET", "max_requests": 8, "schedule_tools": os.Getenv("PROJECT_RELEASE_SCHEDULE_TOOLS") == "1"}}, "agent_audience": "model",
		"limits": map[string]any{"max_mounts_per_project": 8, "idle_after": "10s", "idle_agent_after": "5s", "interval": "100ms"},
	}
	raw, err = json.Marshal(cfg)
	require(t, err)
	require(t, os.WriteFile(filepath.Join(root, "application.json"), raw, 0644))
	jar, err := cookiejar.New(nil)
	require(t, err)
	a := &releaseApp{t: t, ctx: ctx, root: root, binary: filepath.Join(root, "orchestrator"), config: filepath.Join(root, "application.json"), origin: "http://127.0.0.1:18192", uid: uint32(uid), gid: uint32(gid), client: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	t.Cleanup(a.stop)
	t.Cleanup(a.stopGateway)
	candidate := a.cli("release-identity", "--config", a.config)
	if _, err = os.Stat(filepath.Join(root, "state/product.db")); !os.IsNotExist(err) {
		t.Fatal("candidate inspection opened application state", err)
	}
	bootstrap := exec.CommandContext(ctx, a.binary, "account", "bootstrap", "--config", a.config, "--username", "researcher", "--password-stdin")
	bootstrap.Stdin = strings.NewReader("isolated-browser-fixture\n")
	bootstrap.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: a.uid, Gid: a.gid}}
	bootstrapOutput, err := bootstrap.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap failed: %v: %s", err, bootstrapOutput)
	}
	a.start()
	current := a.cli("diagnostics", "--socket", filepath.Join(root, "state/control.sock"))["release_identity"].(map[string]any)
	// Catalog state is separate from the executable deployment identity.
	candidateJSON, _ := json.Marshal(candidate)
	currentJSON, _ := json.Marshal(current)
	liveCandidate := a.cli("release-identity", "--config", a.config)
	liveCandidateJSON, _ := json.Marshal(liveCandidate)
	if !bytes.Equal(candidateJSON, liveCandidateJSON) {
		t.Fatal("inspection changed while owner holds Store lock", candidate, liveCandidate)
	}
	if !bytes.Equal(candidateJSON, currentJSON) {
		t.Fatal("startup identity differs from inspected candidate", candidate, current)
	}
	a.cli("publish", "--socket", filepath.Join(root, "state/control.sock"), "--config", a.config)
	// Serving and candidate inspection need no original manifest, source tree,
	// template, or initial allocation declarations once the database is seeded.
	delete(cfg, "template")
	delete(cfg, "content")
	cfg["project"] = map[string]any{"reachability": cfg["project"].(map[string]any)["reachability"]}
	raw, err = json.Marshal(cfg)
	require(t, err)
	require(t, os.WriteFile(a.config, raw, 0644))
	require(t, os.Remove(filepath.Join(root, "manifest.json")))
	require(t, os.RemoveAll(filepath.Join(root, "content")))
	require(t, os.RemoveAll(filepath.Join(root, "assets")))
	a.startGateway()
	a.request("POST", "/api/auth/login", map[string]string{"username": "researcher", "password": "isolated-browser-fixture"}, 200)
	return a
}

func TestReleasedExecutableOnKhora(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_CHECK_CONTEXT", "1")
	a := newReleasedApp(t)
	socket := filepath.Join(a.root, "state/control.sock")
	status := a.cli("diagnostics", "--socket", socket)
	if status["application_digest"] == "" {
		t.Fatal("release identity absent")
	}
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
	// Wait for the existing idle policy to settle before taking a revocable ready
	// checkpoint. This is a quiescent restart, not active-stream handoff.
	for p := 0; p < 2; p++ {
		base := fmt.Sprintf("/api/projects/release-%d", p)
		a.await(20*time.Second, "pre-release idle pause", func() bool { return a.request("GET", base, nil, 200)["state"] == "paused" })
	}
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
	ready := a.cli("drain-ready", "--socket", socket, "--owner", "release-fixture", "--generation", fmt.Sprint(release["generation"]))
	if ready["phase"] != "ready" {
		t.Fatal(ready)
	}
	// A real owner crash retains the credential session and reconciles durable state.
	start := time.Now()
	a.stop()
	a.start()
	retained := a.cli("drain-status", "--socket", socket)
	if retained["phase"] != "ready" || retained["generation"] != release["generation"] {
		t.Fatal("release fence lost across restart", release, retained)
	}
	a.request("POST", paths[0]+"/turns", map[string]string{"request_id": "during-drain", "content": "blocked until abort"}, 503)
	digest := a.cli("diagnostics", "--socket", socket)["application_digest"].(string)
	activation := a.cli("drain-activate", "--socket", socket, "--owner", "release-fixture", "--generation", fmt.Sprint(release["generation"]), "--target", digest)
	if activation["phase"] != "activating" || activation["target_digest"] != digest {
		t.Fatal(activation)
	}
	a.awaitRetired()
	a.start()
	activating := a.cli("drain-status", "--socket", socket)
	if activating["phase"] != "activating" || activating["activation_instance"] != activation["activation_instance"] {
		t.Fatal(activating)
	}
	a.await(10*time.Second, "replacement completes activation", func() bool {
		command := exec.CommandContext(a.ctx, a.binary, "drain-complete", "--socket", socket, "--owner", "release-fixture", "--generation", fmt.Sprint(release["generation"]))
		raw, err := command.CombinedOutput()
		if err != nil && bytes.Contains(raw, []byte("project has admitted activity")) {
			return false
		}
		require(t, err)
		var finished map[string]any
		require(t, json.Unmarshal(raw, &finished))
		if finished["phase"] != "open" || finished["finished_digest"] != digest {
			t.Fatal(finished)
		}
		return true
	})
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

func (a *releaseApp) awaitRetired() {
	a.t.Helper()
	command := a.command
	ended := make(chan error, 1)
	go func() { ended <- command.Wait() }()
	select {
	case err := <-ended:
		a.command = nil
		require(a.t, err)
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-ended
		a.command = nil
		a.t.Fatal("activated owner did not retire")
	}
}

func TestReleasedActivationTarget(t *testing.T) {
	a := newReleasedApp(t)
	socket := filepath.Join(a.root, "state/control.sock")
	a.request("POST", "/api/projects", map[string]string{"id": "activation", "title": "Activation"}, 201)
	a.request("POST", "/api/projects/activation/sessions", map[string]string{"id": "session", "title": "Session"}, 201)
	base := "/api/projects/activation/sessions/session"
	original := a.request("POST", base+"/turns", map[string]any{"request_id": "held", "content": "retained intent", "held": true}, 201)
	old := a.binary
	a.binary = filepath.Join(a.root, "orchestrator-candidate")
	target := a.cli("release-identity", "--config", a.config)["executable_digest"].(string)
	a.binary = old
	drain := a.cli("drain", "--socket", socket, "--owner", "target-fixture", "--timeout-seconds", "60")
	generation := fmt.Sprint(drain["generation"])
	a.cli("drain-ready", "--socket", socket, "--owner", "target-fixture", "--generation", generation)
	a.cli("drain-activate", "--socket", socket, "--owner", "target-fixture", "--generation", generation, "--target", target)
	a.awaitRetired()
	attempt, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	unexpected := exec.CommandContext(attempt, old, "serve", "--config", a.config)
	unexpected.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: a.uid, Gid: a.gid}}
	raw, err := unexpected.CombinedOutput()
	cancel()
	if err == nil || !bytes.Contains(raw, []byte("differs from the release target")) {
		t.Fatalf("unselected executable not refused: %v %s", err, raw)
	}
	a.binary = filepath.Join(a.root, "orchestrator-candidate")
	a.start()
	a.request("POST", base+"/turns", map[string]string{"request_id": "closed", "content": "blocked"}, 503)
	state := a.cli("drain-complete", "--socket", socket, "--owner", "target-fixture", "--generation", generation)
	if state["phase"] != "open" || state["finished_digest"] != target {
		t.Fatal(state)
	}
	rows := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != original["id"] || rows[0].(map[string]any)["state"] != "held" {
		t.Fatal(rows)
	}
}

func (a *releaseApp) resourceTemplate(kind, id string) map[string]any {
	a.t.Helper()
	for _, value := range a.request("GET", "/api/admin/resource-templates/"+kind, nil, 200)["templates"].([]any) {
		item := value.(map[string]any)
		if item["id"] == id {
			return item
		}
	}
	a.t.Fatalf("missing %s template %s", kind, id)
	return nil
}
func (a *releaseApp) saveResourceTemplate(kind string, before, document map[string]any, requestID string) map[string]any {
	a.t.Helper()
	path := "/api/admin/resource-templates/" + kind + "/" + before["id"].(string)
	body := map[string]any{"preview": true, "request_id": requestID, "expected_revision": before["revision"], "name": before["name"], "description": before["description"], "enabled": before["enabled"], kind: document}
	preview := a.request("PATCH", path, body, 200)
	delete(body, "preview")
	body["expected_dependencies"] = preview["expected_dependencies"]
	return a.request("PATCH", path, body, 200)
}
