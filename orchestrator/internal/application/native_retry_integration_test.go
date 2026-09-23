//go:build khoraintegration

package application_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReleasedNativeRetrySettlement(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	t.Setenv("PROJECT_RELEASE_RETRY_TEST", "1")
	a := newReleasedApp(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
	require(t, err)
	defer db.Close()
	count := func(turn, kind, event string) int {
		var n int
		require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind=? AND (?='' OR json_extract(payload,'$.type')=?)`, turn, kind, event, event).Scan(&n))
		return n
	}
	project := "retry-settlement"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Native retry settlement"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Retry"}, 201)
	first := a.request("POST", base+"/turns", map[string]string{"request_id": "retry", "content": "native-retry-once"}, 201)
	firstID := first["id"].(string)
	a.await(20*time.Second, "native retry backoff", func() bool { return count(firstID, "native", "auto_retry_start") == 1 })
	if count(firstID, "command.completed", "") != 0 {
		t.Fatal("retrying native prompt was completed at agent_end")
	}
	turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
	if turns[0].(map[string]any)["state"] != "running" {
		t.Fatal("retry advertised idle/failed", turns)
	}
	second := a.request("POST", base+"/turns", map[string]string{"request_id": "after-retry", "content": "ordinary queued continuation"}, 201)
	secondID := second["id"].(string)
	if count(secondID, "command.applied", "") != 0 {
		t.Fatal("queued prompt dispatched during native retry")
	}
	a.await(20*time.Second, "retried and queued native completion", func() bool {
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		return len(turns) == 2 && turns[0].(map[string]any)["state"] == "completed" && turns[1].(map[string]any)["state"] == "completed"
	})
	if count(firstID, "command.applied", "") != 1 || count(firstID, "command.completed", "") != 1 || count(firstID, "native", "prompt_end") != 1 {
		t.Fatal("retry changed original command identity or duplicated settlement")
	}
	var nativeEnd, completed int64
	require(t, db.QueryRow(`SELECT sequence FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='prompt_end'`, firstID).Scan(&nativeEnd))
	require(t, db.QueryRow(`SELECT sequence FROM agent_records WHERE turn_id=? AND kind='command.completed'`, firstID).Scan(&completed))
	if completed <= nativeEnd {
		t.Fatal("completion preceded native settlement", nativeEnd, completed)
	}
	third := a.request("POST", base+"/turns", map[string]string{"request_id": "cancel-retry", "content": "native-retry-until-stop"}, 201)
	thirdID := third["id"].(string)
	a.await(10*time.Second, "cancelable retry backoff", func() bool { return count(thirdID, "native", "auto_retry_start") == 1 })
	session := a.request("GET", base, nil, 200)
	started := time.Now()
	a.request("POST", base+"/control", map[string]any{"action": "interrupt", "request_id": "stop-retry", "control_revision": session["control_revision"]}, 202)
	a.await(5*time.Second, "native retry cancellation", func() bool {
		turns := a.request("GET", base+"/turns", nil, 200)["turns"].([]any)
		return len(turns) == 3 && turns[2].(map[string]any)["state"] == "canceled"
	})
	stopMillis := time.Since(started).Milliseconds()
	if count(thirdID, "command.completed", "") != 1 || count(thirdID, "native", "prompt_end") != 1 {
		t.Fatal("retry cancellation did not settle exactly once")
	}
	fourth := a.request("POST", base+"/turns", map[string]string{"request_id": "exhaust-retry", "content": "native-retry-exhaust"}, 201)
	fourthID := fourth["id"].(string)
	a.await(20*time.Second, "native retry exhaustion", func() bool {
		return a.request("GET", base+"/turns", nil, 200)["turns"].([]any)[3].(map[string]any)["state"] == "failed"
	})
	if count(fourthID, "command.applied", "") != 1 || count(fourthID, "command.completed", "") != 1 || count(fourthID, "native", "auto_retry_start") != 2 {
		t.Fatal("retry exhaustion settled before all native attempts")
	}
	var retryEnd, failedEnd int64
	require(t, db.QueryRow(`SELECT max(sequence) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='auto_retry_end'`, fourthID).Scan(&retryEnd))
	require(t, db.QueryRow(`SELECT sequence FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='prompt_end' AND json_extract(payload,'$.outcome')='failed'`, fourthID).Scan(&failedEnd))
	if failedEnd <= retryEnd {
		t.Fatal("failed prompt settled before retry exhaustion", retryEnd, failedEnd)
	}
	raw, err := os.ReadFile(filepath.Join(a.root, "requests.jsonl"))
	require(t, err)
	requests := map[string]int{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var row map[string]string
		require(t, json.Unmarshal(line, &row))
		requests[row["turn"]]++
	}
	if requests[firstID] != 2 || requests[secondID] != 1 || requests[thirdID] != 1 || requests[fourthID] != 3 {
		t.Fatal("unexpected provider attempts or retry after cancellation", requests)
	}
	evidence, _ := json.MarshalIndent(map[string]any{"requests_by_turn": requests, "native_settlement_sequence": nativeEnd, "completion_sequence": completed, "stop_retry_ms": stopMillis, "retry_exhausted_sequence": retryEnd, "failed_settlement_sequence": failedEnd}, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "native-retry.json"), evidence, 0644))
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "retry fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", filepath.Join(a.root, "state/control.sock"))["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Logf("native retry preserved one prompt; cancellation settled in %dms; provider attempts=2/1/1/3", stopMillis)
}

func TestReleasedNativeCompactionSettlement(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	t.Setenv("PROJECT_RELEASE_COMPACTION_TEST", "1")
	a := newReleasedApp(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
	require(t, err)
	defer db.Close()
	project := "compaction-settlement"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Native compaction settlement"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Compaction"}, 201)
	a.request("POST", base+"/turns", map[string]string{"request_id": "seed", "content": "native-compaction-seed"}, 201)
	a.await(20*time.Second, "seed history completion", func() bool {
		return a.request("GET", base+"/turns", nil, 200)["turns"].([]any)[0].(map[string]any)["state"] == "completed"
	})
	turn := a.request("POST", base+"/turns", map[string]string{"request_id": "compact", "content": "native-compaction-trigger"}, 201)
	id := turn["id"].(string)
	a.await(10*time.Second, "post-run compaction", func() bool {
		var n int
		require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='compaction_start'`, id).Scan(&n))
		return n > 0
	})
	var early int
	require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='command.completed'`, id).Scan(&early))
	if early != 0 {
		t.Fatal("prompt completed before compaction settled")
	}
	a.await(10*time.Second, "compaction and original prompt completion", func() bool {
		return a.request("GET", base+"/turns", nil, 200)["turns"].([]any)[1].(map[string]any)["state"] == "completed"
	})
	var compactionEnd, promptEnd, completion int64
	require(t, db.QueryRow(`SELECT max(sequence) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='compaction_end' AND json_extract(payload,'$.result') IS NOT NULL`, id).Scan(&compactionEnd))
	require(t, db.QueryRow(`SELECT sequence FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='prompt_end'`, id).Scan(&promptEnd))
	require(t, db.QueryRow(`SELECT sequence FROM agent_records WHERE turn_id=? AND kind='command.completed'`, id).Scan(&completion))
	if compactionEnd >= promptEnd || promptEnd >= completion {
		t.Fatal("compaction settlement ordering changed", compactionEnd, promptEnd, completion)
	}
	var capture string
	a.await(10*time.Second, "complete native capture export", func() bool {
		if err := db.QueryRow(`SELECT host_path FROM native_captures WHERE turn_id=? AND state<>'guest'`, id).Scan(&capture); err == sql.ErrNoRows {
			return false
		} else {
			require(t, err)
		}
		return capture != ""
	})
	raw, err := os.ReadFile(capture)
	require(t, err)
	if !bytes.Contains(raw, []byte(`"type":"compaction"`)) {
		t.Fatal("native capture omitted final compaction entry")
	}
	// A separate history exercises cancellation both after agent_end and during
	// the next prompt's preflight compaction, before native dispatch acceptance.
	cancelBase := "/api/projects/" + project + "/sessions/cancel"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "cancel", "title": "Cancelable compaction"}, 201)
	a.request("POST", cancelBase+"/turns", map[string]string{"request_id": "seed-cancel", "content": "native-compaction-seed"}, 201)
	a.await(20*time.Second, "cancel fixture seed", func() bool {
		return a.request("GET", cancelBase+"/turns", nil, 200)["turns"].([]any)[0].(map[string]any)["state"] == "completed"
	})
	for index, content := range []string{"native-compaction-trigger", "native-preflight-cancel"} {
		turn := a.request("POST", cancelBase+"/turns", map[string]string{"request_id": fmt.Sprint("cancel-", index), "content": content}, 201)
		turnID := turn["id"].(string)
		a.await(10*time.Second, "cancelable native compaction", func() bool {
			var n int
			require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='compaction_start'`, turnID).Scan(&n))
			return n > 0
		})
		if index == 1 {
			var n int
			require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='command.applied'`, turnID).Scan(&n))
			if n != 0 {
				t.Fatal("fixture missed preflight cancellation window")
			}
		}
		session := a.request("GET", cancelBase, nil, 200)
		a.request("POST", cancelBase+"/control", map[string]any{"action": "interrupt", "request_id": fmt.Sprint("interrupt-", index), "control_revision": session["control_revision"]}, 202)
		a.await(5*time.Second, "compaction cancellation settlement", func() bool {
			return a.request("GET", cancelBase+"/turns", nil, 200)["turns"].([]any)[index+1].(map[string]any)["state"] == "canceled"
		})
		if index == 1 {
			var n int
			require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='agent_start'`, turnID).Scan(&n))
			if n != 0 {
				t.Fatal("canceled preflight still dispatched the user prompt")
			}
		}
	}
	evidence, _ := json.MarshalIndent(map[string]any{"compaction_end_sequence": compactionEnd, "prompt_end_sequence": promptEnd, "command_completed_sequence": completion, "capture_bytes": len(raw)}, "", "  ")
	require(t, os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "native-compaction.json"), evidence, 0644))
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "compaction fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", filepath.Join(a.root, "state/control.sock"))["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Logf("native compaction completed before prompt settlement and capture; sequences=%d/%d/%d", compactionEnd, promptEnd, completion)
}
