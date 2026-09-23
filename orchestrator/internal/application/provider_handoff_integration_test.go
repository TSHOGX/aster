//go:build khoraintegration

package application_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReleasedRealProviderHandoff(t *testing.T) {
	if os.Getenv("PROJECT_PROVIDER_SOCKET") == "" || os.Getenv("PROJECT_PROVIDER_REPORT") == "" || os.Getenv("PROJECT_PROVIDER_MODEL") == "" {
		t.Fatal("explicit host provider relay required")
	}
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "provider-bridge")
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
		t.Fatal("candidate identity did not exercise a compatible executable update")
	}
	type providerState struct {
		Provider, Model, Error              string
		FinishReason                        string    `json:"finish_reason"`
		FirstDelta                          time.Time `json:"first_delta"`
		Started, Ended                      time.Time
		Requests, Status, Deltas            int
		Bytes                               int64
		Done                                bool
		Project, Session, Incarnation, Turn string
	}
	readProvider := func() providerState {
		raw, err := os.ReadFile(os.Getenv("PROJECT_PROVIDER_REPORT"))
		require(t, err)
		var report providerState
		require(t, json.Unmarshal(raw, &report))
		return report
	}
	evidence := map[string]any{"source": source, "candidate": candidate}
	t.Cleanup(func() {
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "real-provider-handoff.json"), raw, 0644)
	})
	initialGateway := a.cli("gateway-status", "--socket", gatewaySocket)
	project := "real-provider-handoff"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Real provider handoff qualification"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Real provider stream"}, 201)
	turn := a.request("POST", base+"/turns", map[string]string{"request_id": "original", "content": "This is a stream continuity test using only synthetic content. Do not call tools or create files. Begin immediately and write 60 numbered lines in plain text. Each line should contain a distinct short observation about elementary physics in roughly ten words. Continue until all lines are written; do not summarize or abbreviate."}, 201)
	turnID := turn["id"].(string)
	a.await(70*time.Second, "real provider stream", func() bool {
		report := readProvider()
		if report.Error != "" || (report.Status != 0 && report.Status != 200) {
			t.Fatalf("provider refused qualification: status=%d category=%s", report.Status, report.Error)
		}
		if !report.FirstDelta.IsZero() && time.Since(report.FirstDelta) >= time.Second {
			if !report.Ended.IsZero() {
				t.Fatal("provider stream finished before the update window")
			}
			evidence["provider_before_update"] = report
			return true
		}
		return false
	})
	target := candidate["executable_digest"].(string)
	drain := a.cli("drain", "--socket", ownerSocket, "--owner", "real-provider-update", "--mode", "owner", "--target", target, "--compatibility", candidate["compatibility_digest"].(string), "--timeout-seconds", "60")
	generation := fmt.Sprint(drain["generation"])
	a.await(5*time.Second, "real-provider handoff readiness", func() bool {
		_, err := exec.CommandContext(a.ctx, a.binary, "drain-ready", "--socket", ownerSocket, "--owner", "real-provider-update", "--generation", generation).Output()
		return err == nil
	})
	started := time.Now()
	a.cli("drain-activate", "--socket", ownerSocket, "--owner", "real-provider-update", "--generation", generation, "--target", target)
	a.awaitRetired()
	a.binary = candidateBinary
	a.start()
	evidence["activation_to_replacement_ms"] = time.Since(started).Milliseconds()
	if time.Since(started) >= 4*time.Second {
		t.Fatal("replacement exceeded qualified authority outage budget")
	}
	a.await(5*time.Second, "real-provider handoff completion", func() bool {
		_, err := exec.CommandContext(a.ctx, a.binary, "drain-complete", "--socket", ownerSocket, "--owner", "real-provider-update", "--generation", generation).Output()
		return err == nil
	})
	gateway := a.cli("gateway-status", "--socket", gatewaySocket)
	if gateway["pid"] != initialGateway["pid"] || gateway["active"] != float64(1) || !readProvider().Ended.IsZero() {
		t.Fatal("original provider stream was not active after owner handoff")
	}
	evidence["gateway_after_update"] = gateway
	a.await(3*time.Minute, "genuine native provider completion", func() bool {
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		if len(turns) != 1 {
			t.Fatal("original native turn was replaced")
		}
		state := turns[0].(map[string]any)["state"]
		if state == "failed" || state == "unknown" || state == "canceled" {
			t.Fatalf("provider turn did not complete: %v", state)
		}
		return state == "completed"
	})
	report := readProvider()
	if report.Requests != 1 || report.Status != 200 || !report.Done || report.Error != "" || report.Deltas < 30 || report.Turn != turnID || report.Incarnation == "" || report.Ended.Sub(report.FirstDelta) < 10*time.Second {
		t.Fatal("provider evidence did not qualify one long completed stream", report.Requests, report.Status, report.Done, report.Error, report.Deltas, report.Ended.Sub(report.FirstDelta))
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
	require(t, err)
	defer db.Close()
	var applied, settled, completed int
	require(t, db.QueryRow(`SELECT sum(kind='command.applied'),sum(kind='native' AND json_extract(payload,'$.type')='prompt_end'),sum(kind='command.completed') FROM agent_records WHERE turn_id=? AND incarnation_id=?`, turnID, report.Incarnation).Scan(&applied, &settled, &completed))
	if applied != 1 || settled != 1 || completed != 1 {
		t.Fatal("native command was resubmitted or lacks settlement", applied, settled, completed)
	}
	evidence["provider_completed"] = report
	evidence["native_applied_settled_completed"] = []int{applied, settled, completed}
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "real-provider fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", ownerSocket)["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Logf("provider=%s model=%s stream=%s deltas=%d bytes=%d replacement=%vms; one original native completion", report.Provider, report.Model, report.Ended.Sub(report.FirstDelta), report.Deltas, report.Bytes, evidence["activation_to_replacement_ms"])
}
