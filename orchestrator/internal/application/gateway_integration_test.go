//go:build khoraintegration

package application_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This qualifies process and native-journal continuity against a deterministic
// model. It does not substitute for the separate external-provider release gate.
func TestReleasedGatewaySurvivesOwnerOutage(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	ownerSocket := filepath.Join(a.root, "state/control.sock")
	gatewaySocket := filepath.Join(a.root, "state/gateway.sock")
	initial := a.cli("gateway-status", "--socket", gatewaySocket)
	project := "gateway-continuity"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Gateway continuity"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Native continuity"}, 201)
	a.request("POST", base+"/turns", map[string]string{"request_id": "original", "content": "long-for-interrupt gateway continuity"}, 201)
	a.await(20*time.Second, "native upstream stream active", func() bool {
		raw, _ := os.ReadFile(filepath.Join(a.root, "requests.jsonl"))
		return len(bytes.TrimSpace(raw)) > 0 && a.cli("gateway-status", "--socket", gatewaySocket)["active"] == float64(1)
	})

	saveEvidence := func() {
		snapshot := map[string]any{}
		if db, e := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro"); e == nil {
			if rows, e := db.Query(`SELECT sequence,kind,payload FROM agent_records ORDER BY sequence LIMIT 32`); e == nil {
				records := []any{}
				for rows.Next() {
					var seq int64
					var kind string
					var raw []byte
					if rows.Scan(&seq, &kind, &raw) == nil {
						var value any
						_ = json.Unmarshal(raw, &value)
						records = append(records, map[string]any{"sequence": seq, "kind": kind, "payload": value})
					}
				}
				rows.Close()
				snapshot["records"] = records
			}
			db.Close()
		}

		command := exec.CommandContext(a.ctx, a.binary, "gateway-status", "--socket", gatewaySocket)
		rawStatus, _ := command.Output()
		var gatewayStatus any
		if json.Unmarshal(rawStatus, &gatewayStatus) == nil {
			snapshot["gateway"] = gatewayStatus
		}
		for _, path := range []string{base, base + "/turns", base + "/view/pages"} {
			response, err := a.client.Get(a.origin + path)
			if err != nil {
				continue
			}
			var value any
			if json.NewDecoder(response.Body).Decode(&value) == nil {
				snapshot[path] = value
			}
			response.Body.Close()
		}
		for _, name := range []string{"requests.jsonl", "completions.jsonl"} {
			raw, _ := os.ReadFile(filepath.Join(a.root, name))
			snapshot[name] = string(raw)
		}
		raw, _ := json.MarshalIndent(snapshot, "", "  ")
		_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "gateway-continuity.json"), raw, 0600)
	}
	t.Cleanup(func() {
		if t.Failed() {
			saveEvidence()
		}
	})
	drain := a.cli("drain", "--socket", ownerSocket, "--owner", "gateway-release", "--timeout-seconds", "60")
	generation := fmt.Sprint(drain["generation"])
	stop := exec.CommandContext(a.ctx, a.binary, "gateway-stop", "--socket", gatewaySocket, "--owner", "gateway-release", "--generation", generation)
	if raw, err := stop.CombinedOutput(); err == nil || !bytes.Contains(raw, []byte("409")) {
		t.Fatalf("active gateway stop was accepted: %v %s", err, raw)
	}
	started := time.Now()
	a.stop()
	// A real interval without an owner exercises the bounded continuation path.
	time.Sleep(250 * time.Millisecond)
	a.start()
	if elapsed := time.Since(started); elapsed >= 4*time.Second {
		t.Fatal("fixture missed authority continuation budget", elapsed)
	}
	after := a.cli("gateway-status", "--socket", gatewaySocket)
	if after["pid"] != initial["pid"] {
		t.Fatal("gateway restarted with owner", initial, after)
	}
	a.await(20*time.Second, "original native turn completion", func() bool {
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		return len(turns) == 1 && turns[0].(map[string]any)["state"] == "completed"
	})
	a.await(5*time.Second, "forwarding acknowledgment", func() bool {
		state := a.cli("gateway-status", "--socket", gatewaySocket)
		return state["active"] == float64(0) && state["pending"] == float64(0)
	})
	var identity string
	for _, name := range []string{"requests.jsonl", "completions.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(a.root, name))
		require(t, err)
		lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
		if len(lines) != 1 {
			t.Fatalf("original stream resubmitted or did not complete (%s): %s", name, raw)
		}
		var row map[string]string
		require(t, json.Unmarshal(lines[0], &row))
		if row["turn"] == "" || row["incarnation"] == "" {
			t.Fatal("native attribution absent", row)
		}
		if identity == "" {
			identity = row["turn"] + "/" + row["incarnation"]
		} else if identity != row["turn"]+"/"+row["incarnation"] {
			t.Fatal("completion changed attribution", row)
		}
	}
	a.await(10*time.Second, "gateway update drain", func() bool {
		cmd := exec.CommandContext(a.ctx, a.binary, "gateway-stop", "--socket", gatewaySocket, "--owner", "gateway-release", "--generation", generation)
		_, err := cmd.CombinedOutput()
		return err == nil
	})
	exited := make(chan error, 1)
	go func() { exited <- a.gateway.Wait() }()
	select {
	case err := <-exited:
		require(t, err)
		a.gateway = nil
	case <-time.After(5 * time.Second):
		t.Fatal("quiescent gateway did not exit")
	}
	a.startGateway()
	if a.cli("gateway-status", "--socket", gatewaySocket)["pid"] == initial["pid"] {
		t.Fatal("gateway replacement did not start")
	}
	a.cli("drain-abort", "--socket", ownerSocket, "--owner", "gateway-release", "--generation", generation)
	a.request("POST", base+"/turns", map[string]string{"request_id": "after-gateway-update", "content": "verify replacement gateway"}, 201)
	a.await(20*time.Second, "replacement gateway native completion", func() bool {
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		return len(turns) == 2 && turns[1].(map[string]any)["state"] == "completed"
	})
	saveEvidence()
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "gateway fixture cleanup", func() bool {
		state := a.cli("diagnostics", "--socket", ownerSocket)["state"].(map[string]any)
		return state["occupied_projects"] == float64(0) && state["projects"].(map[string]any)["deleted"] == float64(1)
	})
	t.Logf("stable gateway PID %v; original native attribution %s; owner outage/restart and gated gateway replacement completed", initial["pid"], identity)
}
