//go:build khoraintegration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/launchplan"
	"harness-platform/orchestrator/internal/store"

	sdk "github.com/TSHOGX/khora/pkg/khora"
)

func TestKhoraDaemonStartsWithoutEmbeddedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	endpoint, template, manifest := os.Getenv("KHORA_INTEGRATION_ENDPOINT"), os.Getenv("KHORA_INTEGRATION_TEMPLATE"), os.Getenv("KHORA_INTEGRATION_MANIFEST")
	if endpoint == "" || template == "" || manifest == "" {
		t.Fatal("isolated Khora endpoint/template/manifest required")
	}
	root, err := os.MkdirTemp("", "daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	client, err := sdk.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := launchplan.Digest(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Authored shared assets are published and verified by the daemon itself, so
	// this exercises real content publication rather than a hand-built volume.
	assetRoot := filepath.Join(root, "assets")
	if err := os.MkdirAll(filepath.Join(assetRoot, "skills"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"AGENTS.md": "Synthetic shared context\n", "skills/probe.md": "probe skill\n"} {
		if err := os.WriteFile(filepath.Join(assetRoot, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	deployment := map[string]any{"version": 1, "backend_name": "daemon-engine", "engine": map[string]any{"endpoint": endpoint, "store_id": info.StoreID}, "manifest": manifest, "manifest_digest": digest, "plans": map[string]any{"sh": map[string]any{"template": template, "cpu_millis": 1000, "memory_bytes": 1 << 30, "disk_bytes": 256 << 20, "workspace_bytes": 64 << 20, "journal_bytes": 64 << 20, "assets": assetRoot}}, "content_bytes": 8 << 20, "authorization": map[string]any{"socket": filepath.Join(root, "authorization.sock"), "peer_uid": os.Getuid(), "group": os.Getgid()}, "prepared_root": filepath.Join(root, "prepared"), "max_active": 2, "inventory_limit": 1000, "lease_ttl": "1m", "poll_interval": "50ms", "event_interval": "50ms", "action_timeout": "5s", "capture_window": "1m", "inventory_interval": "100ms", "idle_interval": "50ms", "idle_threshold": "2s"}
	raw, _ = json.Marshal(deployment)
	deploymentPath := filepath.Join(root, "khora.json")
	if err := os.WriteFile(deploymentPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(root, "harness.yaml")
	if err := os.WriteFile(harnessPath, []byte("harness:\n  max_sessions: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "orchestrator")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v %s", err, output)
	}
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := port.Addr().String()
	port.Close()
	var environment []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HARNESS_") && !strings.HasPrefix(value, "RUNSC_") {
			environment = append(environment, value)
		}
	}
	dbPath := filepath.Join(root, "state", "control.db")
	environment = append(environment, "HARNESS_CONFIG_PATH="+harnessPath, "HARNESS_KHORA_DEPLOYMENT="+deploymentPath, "HARNESS_ORCHESTRATOR_ADDR="+address, "HARNESS_DB_PATH="+dbPath, "HARNESS_RUN_DIR="+filepath.Join(root, "run"))
	httpClient := &http.Client{Timeout: 8 * time.Second}
	var process *exec.Cmd
	var processDone chan error
	logPath := filepath.Join(root, "daemon.log")
	stop := func() {
		if process == nil {
			return
		}
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-processDone:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			_ = process.Process.Kill()
			<-processDone
			t.Error("daemon shutdown timed out")
		}
		process = nil
	}
	defer func() {
		stop()
		if t.Failed() {
			log, _ := os.ReadFile(logPath)
			t.Log(string(log))
		}
	}()
	start := func() {
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		process = exec.CommandContext(ctx, binary, "serve")
		process.Env = environment
		process.Stdout = log
		process.Stderr = log
		if err := process.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		processDone = make(chan error, 1)
		go func(command *exec.Cmd) { err := command.Wait(); log.Close(); processDone <- err }(process)
		for {
			response, err := httpClient.Get("http://" + address + "/healthz")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					break
				}
			}
			select {
			case err := <-processDone:
				process = nil
				t.Fatalf("daemon exited before listening: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	request := func(method, path, body string) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}
	start()
	authorizationClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(root, "authorization.sock"))
	}}}
	defer authorizationClient.CloseIdleConnections()
	authorizationResponse, err := authorizationClient.Post("http://unix/v1/authorize", "application/json", strings.NewReader(`{"backend_id":"unknown","destination":"10.80.0.1:9030","endpoint_id":"data","request_id":"ungranted"}`))
	if err != nil {
		t.Fatal(err)
	}
	authorizationResponse.Body.Close()
	if authorizationResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("ungranted daemon gateway request: %d", authorizationResponse.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "run", "proxy-correlation.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Khora daemon created legacy correlation listener: %v", err)
	}
	code, body := request("POST", "/api/sessions", `{"mode":"shell"}`)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	code, body = request("PUT", "/artifacts/"+session.ID+"/daemon.txt", "daemon-retained-ok\n")
	if code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	preparedPath := filepath.Join(root, "prepared", fmt.Sprintf("%x", sha256.Sum256([]byte("daemon-retained-ok\n"))))
	waitCollected := func() {
		for {
			_, err := os.Stat(preparedPath)
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	waitCollected()
	code, body = request("PUT", "/artifacts/"+session.ID+"/same-content.txt", "daemon-retained-ok\n")
	if code != http.StatusOK {
		t.Fatalf("re-upload collected content: %d %s", code, body)
	}
	waitCollected()
	code, body = request("GET", "/artifacts/"+session.ID+"/same-content.txt", "")
	if code != http.StatusOK || string(body) != "daemon-retained-ok\n" {
		t.Fatalf("collection changed durable workspace: %d %s", code, body)
	}
	reader, err := store.OpenForMaintenance(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer func() {
		stop()
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if env, err := reader.GetSessionEnvironment(cleanup, session.ID); err == nil {
			_ = client.Destroy(cleanup, env.SandboxID)
		}
		for _, purpose := range []string{"workspace", "bridge"} {
			if volume, found, err := reader.GetStorageBinding(cleanup, session.ID, purpose); err == nil && found {
				_ = client.DeleteVolume(cleanup, volume.VolumeID)
			}
		}
		// A published volume pins its staging parent, so the derived volume must go
		// first. Both are disposable preparation for this disposable engine store.
		if rows, err := reader.ListContentPublications(cleanup, info.StoreID); err == nil {
			for _, row := range rows {
				for _, volume := range []string{row.PublishedVolumeID, row.StagingVolumeID} {
					if volume != "" {
						_ = client.DeleteVolume(cleanup, volume)
					}
				}
			}
		}
	}()
	submitted := 0
	send := func() {
		submitted++
		code, body := request("POST", "/api/sessions/"+session.ID+"/messages", `{"content":"cat /workspace/daemon.txt"}`)
		for code == http.StatusConflict {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
			code, body = request("POST", "/api/sessions/"+session.ID+"/messages", `{"content":"cat /workspace/daemon.txt"}`)
		}
		if code != 202 {
			t.Fatalf("send: %d %s", code, body)
		}
		var turn struct {
			ID int64 `json:"turn_id"`
		}
		if err := json.Unmarshal(body, &turn); err != nil {
			t.Fatal(err)
		}
		for {
			status, reason, err := reader.TurnStatus(ctx, session.ID, turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "completed" {
				break
			}
			if status == "failed" || status == "canceled" {
				t.Fatalf("turn: %s %s", status, reason)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	send()
	// The daemon published these bytes itself, so a guest read of the mounted
	// revision is evidence that publication reached a real cold launch.
	assertGuestReads := func(path, want string) {
		submitted++
		code, body := request("POST", "/api/sessions/"+session.ID+"/messages", `{"content":"cat `+path+`"}`)
		for code == http.StatusConflict {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
			code, body = request("POST", "/api/sessions/"+session.ID+"/messages", `{"content":"cat `+path+`"}`)
		}
		if code != http.StatusAccepted {
			t.Fatalf("read published content: %d %s", code, body)
		}
		var turn struct {
			ID int64 `json:"turn_id"`
		}
		if err := json.Unmarshal(body, &turn); err != nil {
			t.Fatal(err)
		}
		for {
			status, reason, err := reader.TurnStatus(ctx, session.ID, turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status == "completed" {
				break
			}
			if status == "failed" || status == "canceled" {
				t.Fatalf("published content turn: %s %s", status, reason)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
		var seen int
		if err := reader.DBForTest().QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='assistant' AND content LIKE ?`, turn.ID, "%"+want+"%").Scan(&seen); err != nil {
			t.Fatal(err)
		}
		if seen == 0 {
			t.Fatalf("guest did not read published content at %s", path)
		}
	}
	assertGuestReads("/root/.agents/AGENTS.md", "Synthetic shared context")
	assertGuestReads("/root/.agents/skills/probe.md", "probe skill")
	first, err := reader.GetSessionEnvironment(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for {
		env, err := reader.GetSessionEnvironment(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if env.ObservedState == "paused" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	diagnostics := exec.CommandContext(ctx, binary, "diagnostics", "--db", dbPath)
	output, err := diagnostics.CombinedOutput()
	if err != nil {
		t.Fatalf("read-only diagnostics: %v %s", err, output)
	}
	var healthSnapshot store.OperationalSnapshot
	if err := json.Unmarshal(output, &healthSnapshot); err != nil || len(healthSnapshot.Workers) == 0 || healthSnapshot.Capacity["retained_environments"] < 1 {
		t.Fatalf("daemon diagnostics incomplete: %s %v", output, err)
	}
	stop()
	start()
	send()
	// A 409 retry must re-submit the same prompt, never enqueue a second turn, so
	// the durable count has to equal exactly what this test submitted.
	var turns int
	if err := reader.DBForTest().QueryRow(`SELECT count(*) FROM turns WHERE session_id=?`, session.ID).Scan(&turns); err != nil || turns != submitted {
		t.Fatalf("busy retries duplicated input: %d durable turns for %d submissions %v", turns, submitted, err)
	}
	latest, err := reader.GetSessionEnvironment(ctx, session.ID)
	if err != nil || latest.SandboxID != first.SandboxID || latest.Epoch <= first.Epoch {
		t.Fatalf("daemon restart lost retained environment: %+v -> %+v %v", first, latest, err)
	}
	code, body = request("POST", "/api/sessions/"+session.ID+"/messages", `{"content":"printf 'retirement-preserved-output\\n'; sleep 60"}`)
	if code != http.StatusAccepted {
		t.Fatalf("retirement turn: %d %s", code, body)
	}
	var interrupted struct {
		ID int64 `json:"turn_id"`
	}
	if err := json.Unmarshal(body, &interrupted); err != nil {
		t.Fatal(err)
	}
	for {
		var output int
		if err := reader.DBForTest().QueryRow(`SELECT count(*) FROM bridge_ingress WHERE session_id=? AND turn_id=? AND type='emit_output'`, session.ID, interrupted.ID).Scan(&output); err != nil {
			t.Fatal(err)
		}
		if output > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	code, body = request("DELETE", "/api/sessions/"+session.ID, "")
	if code != http.StatusOK && code != http.StatusServiceUnavailable {
		t.Fatalf("retirement request: %d %s", code, body)
	}
	for {
		sessionState, err := reader.GetSession(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if sessionState.Status == "destroyed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	code, body = request("DELETE", "/api/sessions/"+session.ID, "")
	if code != http.StatusOK {
		t.Fatalf("idempotent retirement: %d %s", code, body)
	}
	retained, err := client.Get(ctx, latest.SandboxID)
	if err != nil || string(retained.State) != "paused" || retained.PlacementState != "lost" {
		t.Fatalf("product deletion lost retained storage or writers: %+v %v", retained, err)
	}
	var preserved int
	if err := reader.DBForTest().QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='assistant' AND content LIKE '%retirement-preserved-output%'`, interrupted.ID).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("retirement lost interrupted transcript: %d %v", preserved, err)
	}
	code, body = request("POST", "/api/sessions", `{"mode":"shell"}`)
	if code != http.StatusCreated {
		t.Fatalf("unprovisioned retirement fixture: %d %s", code, body)
	}
	var empty struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &empty); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		code, body = request("DELETE", "/api/sessions/"+empty.ID, "")
		if code != http.StatusOK {
			t.Fatalf("unprovisioned retirement: %d %s", code, body)
		}
	}
	for _, table := range []string{"network_profiles", "runtime_resource_instances", "session_driver_homes"} {
		var count int
		if err := reader.DBForTest().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("daemon populated legacy %s: %d %v", table, count, err)
		}
	}
	t.Logf("daemon retained sandbox=%s epoch=%d->%d", latest.SandboxID, first.Epoch, latest.Epoch)
}
