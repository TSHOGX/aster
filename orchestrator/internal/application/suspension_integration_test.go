//go:build khoraintegration

package application_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestReleasedAccountSuspensionReconcilesAfterOwnerRestart(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	admin := a.request("GET", "/api/auth/session", nil, 200)
	policy := a.request("POST", "/api/admin/policies", map[string]any{"request_id": "public-policy", "name": "Suspension publishers", "statements": map[string]string{"shares.public.create": "allow"}}, 200)["policy"].(map[string]any)
	group := a.request("POST", "/api/admin/groups", map[string]any{"request_id": "public-group", "name": "Suspension publishers"}, 200)["group"].(map[string]any)
	a.request("PUT", "/api/admin/groups/"+group["id"].(string)+"/policies", map[string]any{"request_id": "bind-public", "expected_revision": group["revision"], "policy_ids": []string{policy["id"].(string)}}, 200)
	created := a.request("POST", "/api/admin/users", map[string]any{"request_id": "suspended-owner", "username": "suspended-owner", "display_name": "Suspended owner", "role": "user", "group_ids": []string{"standard-users", group["id"].(string)}}, 201)
	user := created["user"].(map[string]any)
	owner := *a
	jar, err := cookiejar.New(nil)
	require(t, err)
	owner.client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	owner.request("POST", "/api/auth/login", map[string]string{"username": "suspended-owner", "password": created["temporary_password"].(string)}, 200)
	owner.request("POST", "/api/auth/password/setup", map[string]string{"new_password": "permanent-suspension-password"}, 200)
	base := "/api/projects/suspension-active"
	cold := "/api/projects/suspension-cold"
	for _, project := range []string{"suspension-active", "suspension-cold"} {
		owner.request("POST", "/api/projects", map[string]string{"id": project, "title": project}, 201)
	}
	owner.writeFile(base+"/files/content?path=retained.txt", "private retained bytes", "active-upload")
	owner.writeFile(cold+"/files/content?path=cold.txt", "cold retained bytes", "cold-upload")
	session := base + "/sessions/native"
	owner.request("POST", base+"/sessions", map[string]string{"id": "native", "title": "Suspension native"}, 201)
	owner.request("POST", session+"/readers", map[string]any{"reader_id": admin["id"]}, 204)
	share := owner.request("POST", session+"/shares", map[string]any{"paths": []string{"retained.txt"}}, 201)
	task := owner.request("POST", base+"/schedules", map[string]any{"id": "suspended-schedule", "request_id": "scheduled-before-suspend", "title": "Suspended task", "prompt": "Never run without reactivation", "trigger_kind": "interval", "cron": "@every 60m", "timezone": "UTC", "state": "active"}, 201)
	owner.request("POST", session+"/turns", map[string]string{"request_id": "running-native", "content": "long-for-owner-handoff"}, 201)
	owner.await(25*time.Second, "native running", func() bool {
		turns := owner.request("GET", session+"/turns", nil, 200)["turns"].([]any)
		return len(turns) > 0 && turns[0].(map[string]any)["state"] == "running"
	})
	owner.request("POST", session+"/turns", map[string]string{"request_id": "queued-native", "content": "Queued until explicit release"}, 201)
	owner.request("POST", base+"/terminals", map[string]any{"id": "pty", "title": "Suspended terminal", "rows": 24, "cols": 80}, 201)
	terminal := base + "/terminals/pty"
	owner.request("POST", terminal+"/start", nil, 0)
	owner.await(15*time.Second, "terminal running", func() bool { return owner.request("GET", terminal, nil, 200)["state"] == "running" })
	conn := owner.terminal(terminal)
	defer conn.Close()
	require(t, conn.WriteMessage(websocket.BinaryMessage, []byte("while :; do echo suspension-live; sleep 0.2; done\n")))
	original := a.request("GET", "/api/admin/projects/suspension-active", nil, 200)
	beforeCold := a.request("GET", "/api/admin/projects/suspension-cold", nil, 200)
	// Freeze only this disposable test engine. API/identity/connection revocation
	// remains live, while runtime pause cannot yet produce completion evidence.
	pid, err := strconv.Atoi(os.Getenv("PROJECT_TEST_ENGINE_PID"))
	require(t, err)
	require(t, syscall.Kill(pid, syscall.SIGSTOP))
	defer syscall.Kill(pid, syscall.SIGCONT)
	current := a.request("GET", "/api/admin/users/"+user["id"].(string), nil, 200)
	input := map[string]any{"request_id": "suspend-mixed", "expected_revision": current["revision"]}
	start := time.Now()
	accepted := a.request("POST", "/api/admin/users/"+user["id"].(string)+"/suspend", input, 202)
	operation := accepted["operation"].(map[string]any)
	require(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}
	if network, ok := err.(interface{ Timeout() bool }); ok && network.Timeout() {
		t.Fatal("terminal authority did not close within five seconds")
	}
	revocation := time.Since(start)
	owner.request("GET", "/api/auth/session", nil, 401)
	owner.request("GET", "/api/shared/"+share["token"].(string), nil, 404)
	opPath := "/api/admin/operations/" + operation["id"].(string)
	a.await(18*time.Second, "unknown stop outcome while engine unavailable", func() bool { return a.request("GET", opPath, nil, 200)["state"] == "unknown" })
	unknown := a.request("GET", opPath, nil, 200)
	if unknown["completed_targets"] == unknown["total_targets"] {
		t.Fatal("unavailable engine falsely completed", unknown)
	}
	replay := a.request("POST", "/api/admin/users/"+user["id"].(string)+"/suspend", input, 202)
	if replay["operation"].(map[string]any)["id"] != operation["id"] {
		t.Fatal("suspension replay forked operation")
	}
	a.stop()
	require(t, syscall.Kill(pid, syscall.SIGCONT))
	a.start()
	a.await(35*time.Second, "suspension after owner replacement", func() bool { return a.request("GET", opPath, nil, 200)["state"] == "succeeded" })
	active := a.request("GET", "/api/admin/projects/suspension-active", nil, 200)
	heldCold := a.request("GET", "/api/admin/projects/suspension-cold", nil, 200)
	if active["state"] != "paused" || active["execution_suspended"] != true || active["binding"].(map[string]any)["occupied"] != false || active["observation"].(map[string]any)["state"] != "paused" {
		t.Fatal(active)
	}
	if heldCold["allocated_bytes"] != beforeCold["allocated_bytes"] || heldCold["reserved_bytes"] != beforeCold["reserved_bytes"] || heldCold["binding"].(map[string]any)["sandbox_id"] != nil {
		t.Fatal("cold storage changed", heldCold)
	}
	suspendedUser := accepted["user"].(map[string]any)
	a.request("POST", "/api/admin/users/"+user["id"].(string)+"/enable", map[string]any{"request_id": "enable-mixed", "expected_revision": suspendedUser["revision"]}, 202)
	owner.request("POST", "/api/auth/login", map[string]string{"username": "suspended-owner", "password": "permanent-suspension-password"}, 200)
	owner.request("POST", session+"/turns", map[string]string{"request_id": "must-resume-first", "content": "must not start"}, 403)
	owner.request("GET", "/api/shared/"+share["token"].(string), nil, 404)
	for path, expected := range map[string]string{base + "/files/content?path=retained.txt": "private retained bytes", cold + "/files/content?path=cold.txt": "cold retained bytes"} {
		response, err := owner.client.Get(owner.origin + path)
		require(t, err)
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		require(t, err)
		if response.StatusCode != 200 || string(data) != expected {
			t.Fatal(path, response.StatusCode, string(data))
		}
	}
	taskAfter := owner.request("GET", base+"/schedules/"+task["id"].(string), nil, 200)
	if taskAfter["state"] != "paused" || taskAfter["last_error"] != "account_suspended" {
		t.Fatal(taskAfter)
	}
	prompts := owner.request("GET", session+"/turns", nil, 200)["turns"].([]any)
	queuedHeld := false
	for _, item := range prompts {
		prompt := item.(map[string]any)
		if prompt["request_id"] == "queued-native" {
			queuedHeld = prompt["state"] == "held" && prompt["dispatch_error"] == "account_suspended"
		}
	}
	if !queuedHeld {
		t.Fatal("queued work resumed on enable", prompts)
	}
	owner.request("GET", session+"/view/pages", nil, 200)
	owner.request("POST", base+"/resume", nil, 202)
	owner.await(25*time.Second, "explicit owner resume", func() bool { return owner.request("GET", base, nil, 200)["state"] == "active" })
	resumed := a.request("GET", "/api/admin/projects/suspension-active", nil, 200)
	if resumed["execution_suspended"] != false || !reflect.DeepEqual(resumed["sandbox_release"], original["sandbox_release"]) {
		t.Fatal(resumed)
	}
	slots := func(v map[string]any) map[string]any {
		result := map[string]any{}
		for _, item := range v["binding"].(map[string]any)["slots"].([]any) {
			slot := item.(map[string]any)
			result[slot["key"].(string)] = []any{slot["volume_id"], slot["destination"], slot["capacity_bytes"], slot["source"]}
		}
		return result
	}
	if !reflect.DeepEqual(slots(resumed), slots(original)) {
		t.Fatal("resume altered saved slots")
	}
	adminControl := func(action, requestID string) {
		t.Helper()
		target := a.request("GET", "/api/admin/projects/suspension-active", nil, 200)
		input := map[string]any{"request_id": requestID, "expected_revision": target["revision"], "interruption_acknowledged": true}
		if action == "apply-network" {
			input["expected_network_revision"] = target["desired_network"].(map[string]any)["revision"]
		}
		accepted := a.request("POST", "/api/admin/projects/suspension-active/"+action, input, 202)
		id := accepted["operation"].(map[string]any)["id"].(string)
		replay := a.request("POST", "/api/admin/projects/suspension-active/"+action, input, 202)
		if replay["operation"].(map[string]any)["id"] != id || replay["replayed"] != true {
			t.Fatal(replay)
		}
		a.await(30*time.Second, "admin "+action+" observed completion", func() bool {
			operation := a.request("GET", "/api/admin/operations/"+id, nil, 200)
			if operation["state"] == "failed" {
				t.Fatal(operation)
			}
			return operation["state"] == "succeeded"
		})
	}
	adminControl("stop", "cross-user-stop")
	stopped := a.request("GET", "/api/admin/projects/suspension-active", nil, 200)
	if stopped["state"] != "paused" || stopped["execution_suspended"] != true || stopped["execution_hold_reason"] != "admin_stop" {
		t.Fatal(stopped)
	}
	owner.request("POST", session+"/turns", map[string]string{"request_id": "admin-stop-holds", "content": "must stay stopped"}, 403)
	adminControl("restart", "cross-user-restart")
	netPath := "/api/admin/users/" + user["id"].(string) + "/network"
	network := a.request("GET", netPath, nil, 200)
	changed := a.request("PATCH", netPath, map[string]any{"request_id": "network-before-apply", "expected_revision": network["configuration_revision"], "expected_default_revision": network["default_configuration_revision"], "mode": "override", "direct": []any{}, "resolvers": []string{"192.0.2.1"}}, 200)
	adminControl("apply-network", "cross-user-network-apply")
	applied := a.request("GET", "/api/admin/projects/suspension-active", nil, 200)
	if applied["network_pending"] != false || applied["saved_network_revision"] != changed["desired"].(map[string]any)["revision"] || !reflect.DeepEqual(slots(applied), slots(original)) {
		t.Fatal(applied)
	}
	adminControl("pause", "cross-user-pause")
	// Deletion remains an explicit independent reduction, including the still-held
	// cold Project. It never deletes the shared source publications.
	for _, path := range []string{base, cold} {
		p := owner.request("GET", path, nil, 200)
		owner.request("DELETE", fmt.Sprintf("%s?revision=%.0f", path, p["revision"]), nil, 202)
	}
	a.await(35*time.Second, "suspension Projects cleanup", func() bool {
		return a.cli("diagnostics", "--socket", filepath.Join(a.root, "state/control.sock"))["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == float64(2)
	})
	raw, _ := json.MarshalIndent(map[string]any{"revocation_ms": revocation.Milliseconds(), "unknown_while_engine_unavailable": true, "owner_restart_same_operation": operation["id"], "preserved_cold_reservations": true, "explicit_resume_retains_slots": true}, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "suspension.json"), raw, 0644))
}
