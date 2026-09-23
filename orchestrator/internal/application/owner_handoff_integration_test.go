//go:build khoraintegration

package application_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleasedOwnerHandoff(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	ownerSocket := filepath.Join(a.root, "state/control.sock")
	gatewaySocket := filepath.Join(a.root, "state/gateway.sock")
	candidateBinary := filepath.Join(a.root, "orchestrator-candidate")
	raw, err := exec.CommandContext(a.ctx, candidateBinary, "release-identity", "--config", a.config).Output()
	require(t, err)
	var candidate map[string]any
	require(t, json.Unmarshal(raw, &candidate))
	source := a.cli("diagnostics", "--socket", ownerSocket)["release_identity"].(map[string]any)
	if source["executable_digest"] == candidate["executable_digest"] || source["compatibility_digest"] != candidate["compatibility_digest"] {
		t.Fatal("fixture requires distinct executables with the same contract", source, candidate)
	}
	evidence := map[string]any{"source": source, "candidate": candidate}
	t.Cleanup(func() {
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "owner-handoff.json"), raw, 0644)
	})
	initialGateway := a.cli("gateway-status", "--socket", gatewaySocket)
	project := "active-owner-update"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Active owner update"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Retained native turn"}, 201)
	turn := a.request("POST", base+"/turns", map[string]string{"request_id": "original", "content": "long-for-owner-handoff"}, 201)
	a.await(20*time.Second, "original applied turn and upstream request", func() bool {
		raw, _ := os.ReadFile(filepath.Join(a.root, "requests.jsonl"))
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		return len(bytes.TrimSpace(raw)) > 0 && len(turns) == 1 && turns[0].(map[string]any)["state"] == "running"
	})
	target := candidate["executable_digest"].(string)
	drain := a.cli("drain", "--socket", ownerSocket, "--owner", "active-owner", "--mode", "owner", "--target", target, "--compatibility", candidate["compatibility_digest"].(string), "--timeout-seconds", "60")
	if drain["mode"] != "owner" || drain["phase"] != "draining" {
		t.Fatal(drain)
	}
	generation := fmt.Sprint(drain["generation"])
	a.request("POST", base+"/turns", map[string]string{"request_id": "not-admitted", "content": "must remain fenced"}, 503)
	a.request("POST", base+"/turns", map[string]any{"request_id": "held-intent", "content": "preserved intent", "held": true}, 201)
	a.await(5*time.Second, "owner handoff readiness", func() bool {
		raw, err := exec.CommandContext(a.ctx, a.binary, "drain-ready", "--socket", ownerSocket, "--owner", "active-owner", "--generation", generation).CombinedOutput()
		if err != nil {
			evidence["last_ready_error"] = string(raw)
			return false
		}
		var ready map[string]any
		require(t, json.Unmarshal(raw, &ready))
		evidence["ready"] = ready
		return ready["phase"] == "ready"
	})
	// Owner-ready can retain a running turn even between upstream requests, so it
	// must never authorize a gateway binary shutdown.
	stop := exec.CommandContext(a.ctx, a.binary, "gateway-stop", "--socket", gatewaySocket, "--owner", "active-owner", "--generation", generation)
	if raw, err = stop.CombinedOutput(); err == nil || !bytes.Contains(raw, []byte("409")) {
		t.Fatalf("gateway accepted owner-only readiness: %v %s", err, raw)
	}
	started := time.Now()
	activation := a.cli("drain-activate", "--socket", ownerSocket, "--owner", "active-owner", "--generation", generation, "--target", target)
	if activation["phase"] != "activating" {
		t.Fatal(activation)
	}
	a.awaitRetired()
	a.binary = candidateBinary
	a.start()
	evidence["activation_to_replacement_ms"] = time.Since(started).Milliseconds()
	if elapsed := time.Since(started); elapsed >= 4*time.Second {
		t.Fatal("replacement exceeded fixture authority budget", elapsed)
	}
	current := a.cli("diagnostics", "--socket", ownerSocket)
	if current["application_digest"] != target || current["release"].(map[string]any)["phase"] != "activating" {
		t.Fatal("unexpected replacement or premature admission", current)
	}
	a.await(5*time.Second, "replacement completes owner handoff", func() bool {
		raw, err := exec.CommandContext(a.ctx, a.binary, "drain-complete", "--socket", ownerSocket, "--owner", "active-owner", "--generation", generation).CombinedOutput()
		if err != nil {
			evidence["last_complete_error"] = string(raw)
			return false
		}
		var completed map[string]any
		require(t, json.Unmarshal(raw, &completed))
		evidence["completed"] = completed
		return completed["phase"] == "open" && completed["finished_digest"] == target
	})
	gateway := a.cli("gateway-status", "--socket", gatewaySocket)
	if gateway["pid"] != initialGateway["pid"] || gateway["active"] != float64(1) {
		t.Fatal("update did not retain the original active gateway stream", gateway, initialGateway)
	}
	evidence["gateway_after_handoff"] = gateway
	a.await(20*time.Second, "original native completion on replacement", func() bool {
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		if len(turns) != 2 {
			t.Fatal("accepted turn set changed", turns)
		}
		completed := false
		for _, raw := range turns {
			row := raw.(map[string]any)
			if row["request_id"] == "held-intent" && row["state"] != "held" {
				t.Fatal("held intent dispatched", row)
			}
			if row["request_id"] == "original" && row["state"] == "completed" {
				completed = true
			}
		}
		return completed
	})
	var identity string
	for _, name := range []string{"requests.jsonl", "completions.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(a.root, name))
		require(t, err)
		lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
		if len(lines) != 1 {
			t.Fatalf("original request resubmitted or did not complete: %s %s", name, raw)
		}
		var row map[string]string
		require(t, json.Unmarshal(lines[0], &row))
		if row["turn"] != turn["id"] || row["incarnation"] == "" {
			t.Fatal("original native identity changed", turn, row)
		}
		if identity == "" {
			identity = row["incarnation"]
		} else if identity != row["incarnation"] {
			t.Fatal("completion moved to another incarnation", row)
		}
		evidence[name] = row
	}
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "owner handoff fixture cleanup", func() bool {
		state := a.cli("diagnostics", "--socket", ownerSocket)["state"].(map[string]any)
		return state["occupied_projects"] == float64(0) && state["projects"].(map[string]any)["deleted"] == float64(1)
	})
	t.Logf("distinct candidate activated in %v ms with gateway PID %v and original native incarnation %s", evidence["activation_to_replacement_ms"], initialGateway["pid"], identity)
}
