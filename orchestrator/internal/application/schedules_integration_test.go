//go:build khoraintegration

package application_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestReleasedNativeSchedules(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	t.Setenv("PROJECT_RELEASE_SCHEDULE_TOOLS", "1")
	a := newReleasedApp(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
	require(t, err)
	defer db.Close()
	project := "native-schedules"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Native schedules"}, 201)
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Scheduling"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	prompt := func(name string, args any) string {
		raw, err := json.Marshal(map[string]any{"name": "schedules_" + name, "arguments": args})
		require(t, err)
		return "schedule-tool-fixture " + string(raw)
	}
	call := func(key, name string, args any) {
		turn := a.request("POST", base+"/turns", map[string]string{"request_id": key, "content": prompt(name, args)}, 201)
		a.await(30*time.Second, "native tool "+name, func() bool {
			var completed int
			require(t, db.QueryRow(`SELECT count(*) FROM execution_runs WHERE prompt_id=? AND outcome='completed'`, turn["id"]).Scan(&completed))
			return completed == 1
		})
		var failures, results int
		require(t, db.QueryRow(`SELECT count(*),coalesce(sum(json_extract(payload,'$.isError')=1),0) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='tool_execution_end'`, turn["id"]).Scan(&results, &failures))
		if results != 1 || failures != 0 {
			t.Fatalf("tool %s: results=%d failures=%d", name, results, failures)
		}
	}
	task := func(title string) map[string]any {
		var id string
		require(t, db.QueryRow(`SELECT id FROM schedules WHERE project_id=? AND title=?`, project, title).Scan(&id))
		return a.request("GET", "/api/projects/"+project+"/schedules/"+id, nil, 200)
	}
	call("create-once", "create", map[string]any{"title": "Once", "prompt": "ordinary scheduled result", "trigger": map[string]any{"kind": "at", "timezone": "UTC", "at": time.Now().Add(12 * time.Second).UTC().Format(time.RFC3339)}, "notify": true})
	once := task("Once")
	if once["origin_session_id"] != "native" || once["origin_tool_call_id"] != "schedule-call" {
		t.Fatal("native source missing", once)
	}
	call("list", "list", map[string]any{})
	call("inspect", "get", map[string]any{"id": once["id"]})
	a.await(35*time.Second, "one-shot admission and result notice", func() bool {
		var n int
		require(t, db.QueryRow(`SELECT count(*) FROM schedule_runs r JOIN execution_runs t ON t.prompt_id=r.turn_id JOIN notifications n ON n.id='turn/'||r.turn_id WHERE r.schedule_id=? AND t.outcome='completed'`, once["id"]).Scan(&n))
		return n == 1
	})
	once = task("Once")
	if once["state"] != "exhausted" || once["admitted_count"] != float64(1) {
		t.Fatal("one-shot did not consume exactly once", once)
	}
	call("create-recurring", "create", map[string]any{"title": "Recurring", "prompt": "recurring result", "target": map[string]any{"kind": "new"}, "trigger": map[string]any{"kind": "interval", "minutes": 60, "timezone": "UTC"}})
	recurring := task("Recurring")
	if recurring["session_id"] != nil {
		t.Fatal("new Session target was not retained", recurring)
	}
	call("edit", "update", map[string]any{"id": recurring["id"], "revision": recurring["revision"], "title": "Edited"})
	recurring = task("Edited")
	call("cancel", "cancel", map[string]any{"id": recurring["id"], "revision": recurring["revision"]})
	if task("Edited")["state"] != "canceled" {
		t.Fatal("native cancel not persisted")
	}
	call("bounded", "create", map[string]any{"title": "Bounded", "prompt": prompt("finish", map[string]any{}), "target": map[string]any{"kind": "new"}, "trigger": map[string]any{"kind": "interval", "minutes": 1, "timezone": "UTC"}, "check": map[string]any{"max_occurrences": 1, "starts_at": time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339), "expires_at": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339), "notification_policy": "on_finish"}})
	bounded := task("Bounded")
	a.await(35*time.Second, "host-bound finish and durable delivery", func() bool {
		var n int
		require(t, db.QueryRow(`SELECT count(*) FROM schedules s JOIN schedule_runs r ON r.schedule_id=s.id JOIN execution_runs t ON t.prompt_id=r.turn_id JOIN notifications n ON n.id='turn/'||r.turn_id WHERE s.id=? AND s.end_reason='agent_finished' AND s.completed_by_run_id IS NOT NULL AND t.outcome='completed'`, bounded["id"]).Scan(&n))
		return n == 1
	})
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "schedule fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", filepath.Join(a.root, "state/control.sock"))["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Log("real Pi/guest route: create/list/get/update/cancel, one-shot delivery, new-Session bounded finish passed")
}
