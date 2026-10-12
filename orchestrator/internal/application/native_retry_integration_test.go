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
	// Execution control metadata remains authoritative while content projection
	// is building (202); waiting for a 200 is unrelated to native control.
	head := a.request("GET", base+"/view/head", nil, 0)
	run := head["active_run"].(map[string]any)
	started := time.Now()
	a.request("POST", base+"/control", map[string]any{"action": "stop", "request_id": "stop-retry", "target_run_id": run["id"], "target_incarnation_id": run["incarnation_id"], "target_owner": run["owner"]}, 202)
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
		head := a.request("GET", cancelBase+"/view/head", nil, 0)
		run := head["active_run"].(map[string]any)
		a.request("POST", cancelBase+"/control", map[string]any{"action": "stop", "request_id": fmt.Sprint("stop-", index), "target_run_id": run["id"], "target_incarnation_id": run["incarnation_id"], "target_owner": run["owner"]}, 202)
		want := "canceled"
		if index == 1 {
			want = "held" // Preflight ended before this input reached the Agent.
		}
		observed := ""
		a.await(5*time.Second, "compaction cancellation settlement", func() bool {
			turns := a.request("GET", cancelBase+"/turns", nil, 200)["turns"].([]any)
			state := turns[index+1].(map[string]any)["state"].(string)
			if state != observed {
				t.Logf("compaction cancellation %d state=%s", index, state)
				observed = state
			}
			return state == want
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

func TestReleasedQueuedSteerConsumption(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
	require(t, err)
	defer db.Close()
	project := "queued-steer"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Queued Steer"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Steer"}, 201)
	first := a.request("POST", base+"/turns", map[string]string{"request_id": "normal", "content": "long-for-interrupt"}, 201)
	firstID := first["id"].(string)
	a.await(10*time.Second, "first native response streaming", func() bool {
		var count int
		require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='message_start' AND json_extract(payload,'$.message.role')='assistant'`, firstID).Scan(&count))
		return count > 0
	})
	// Execution control metadata remains authoritative while content projection
	// is building (202); waiting for a 200 is unrelated to native control.
	head := a.request("GET", base+"/view/head", nil, 0)
	run := head["active_run"].(map[string]any)
	source := a.request("POST", base+"/turns", map[string]any{"request_id": "held-source", "content": "second question", "held": true}, 201)
	sourceID := source["id"].(string)
	receipt := a.request("POST", base+"/control", map[string]any{
		"action": "steer", "request_id": "steer-source", "source": sourceID,
		"expected_prompt_revision": source["prompt_revision"],
		"target_run_id":            run["id"], "target_incarnation_id": run["incarnation_id"], "target_owner": run["owner"],
	}, 202)
	if receipt["run_id"] != run["id"] || receipt["delivery_id"] == "" {
		t.Fatal("Steer was not bound to the original run", receipt)
	}
	var token string
	require(t, db.QueryRow(`SELECT input_token FROM input_deliveries WHERE id=?`, receipt["delivery_id"]).Scan(&token))
	a.await(20*time.Second, "correlated native Steer consumption", func() bool {
		var state string
		require(t, db.QueryRow(`SELECT state FROM input_deliveries WHERE input_token=?`, token).Scan(&state))
		return state == "consumed"
	})
	a.await(20*time.Second, "steered run settlement and display", func() bool {
		var state string
		require(t, db.QueryRow(`SELECT state FROM execution_runs WHERE id=?`, run["id"]).Scan(&state))
		if state != "settled" {
			return false
		}
		var count int
		require(t, db.QueryRow(`SELECT count(*) FROM conversation_items WHERE session_id='native' AND turn_id=? AND json_extract(data,'$.kind')='assistant'`, sourceID).Scan(&count))
		return count > 0
	})
	var firstAnswer, steerQuestion, secondAnswer int64
	require(t, db.QueryRow(`SELECT min(position) FROM conversation_items WHERE session_id='native' AND turn_id=? AND json_extract(data,'$.kind')='assistant'`, firstID).Scan(&firstAnswer))
	require(t, db.QueryRow(`SELECT min(position) FROM conversation_items WHERE session_id='native' AND turn_id=? AND json_extract(data,'$.kind')='user'`, sourceID).Scan(&steerQuestion))
	require(t, db.QueryRow(`SELECT min(position) FROM conversation_items WHERE session_id='native' AND turn_id=? AND json_extract(data,'$.kind')='assistant'`, sourceID).Scan(&secondAnswer))
	if !(firstAnswer < steerQuestion && steerQuestion < secondAnswer) {
		t.Fatal("Steer question did not precede its answer", firstAnswer, steerQuestion, secondAnswer)
	}
	var segments, runs int
	require(t, db.QueryRow(`SELECT count(DISTINCT json_extract(data,'$.segment_id')),count(DISTINCT json_extract(data,'$.run_id')) FROM conversation_items WHERE session_id='native' AND turn_id IN (?,?) AND json_extract(data,'$.kind')='assistant'`, firstID, sourceID).Scan(&segments, &runs))
	if segments != 2 || runs != 1 {
		t.Fatal("Steer started another native run or reused the first segment", segments, runs)
	}
	var consumedRecords int
	require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE kind='native' AND json_extract(payload,'$.type')='input_consumed' AND json_extract(payload,'$.input_token')=?`, token).Scan(&consumedRecords))
	if consumedRecords != 1 {
		t.Fatal("Steer consumption was not journaled exactly once", consumedRecords)
	}
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "Steer fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", filepath.Join(a.root, "state/control.sock"))["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Logf("Steer consumed once in run %s; display positions %d/%d/%d", run["id"], firstAnswer, steerQuestion, secondAnswer)
}

func TestReleasedQueuedSteerStopRemovesUnconsumedInput(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(a.root, "state/product.db")+"?mode=ro")
	require(t, err)
	defer db.Close()
	project := "queued-steer-stop"
	a.request("POST", "/api/projects", map[string]string{"id": project, "title": "Stopped Steer"}, 201)
	base := "/api/projects/" + project + "/sessions/native"
	a.request("POST", "/api/projects/"+project+"/sessions", map[string]string{"id": "native", "title": "Stop"}, 201)
	first := a.request("POST", base+"/turns", map[string]string{"request_id": "normal", "content": "long-for-interrupt"}, 201)
	firstID := first["id"].(string)
	a.await(10*time.Second, "first native response streaming", func() bool {
		var count int
		require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE turn_id=? AND kind='native' AND json_extract(payload,'$.type')='message_start' AND json_extract(payload,'$.message.role')='assistant'`, firstID).Scan(&count))
		return count > 0
	})
	// Execution control metadata remains authoritative while content projection
	// is building (202); waiting for a 200 is unrelated to native control.
	head := a.request("GET", base+"/view/head", nil, 0)
	run := head["active_run"].(map[string]any)
	source := a.request("POST", base+"/turns", map[string]any{"request_id": "held-source", "content": "must remain held", "held": true}, 201)
	sourceID := source["id"].(string)
	receipt := a.request("POST", base+"/control", map[string]any{
		"action": "steer", "request_id": "steer-before-stop", "source": sourceID,
		"expected_prompt_revision": source["prompt_revision"],
		"target_run_id":            run["id"], "target_incarnation_id": run["incarnation_id"], "target_owner": run["owner"],
	}, 202)
	var token string
	require(t, db.QueryRow(`SELECT input_token FROM input_deliveries WHERE id=?`, receipt["delivery_id"]).Scan(&token))
	a.await(5*time.Second, "native Steer acceptance before Stop", func() bool {
		var state string
		require(t, db.QueryRow(`SELECT state FROM input_deliveries WHERE input_token=?`, token).Scan(&state))
		return state == "native_queued"
	})
	a.request("POST", base+"/control", map[string]any{
		"action": "stop", "request_id": "stop-before-consumption",
		"target_run_id": run["id"], "target_incarnation_id": run["incarnation_id"], "target_owner": run["owner"],
	}, 202)
	a.await(10*time.Second, "native Stop reconciliation", func() bool {
		var state string
		require(t, db.QueryRow(`SELECT state FROM input_deliveries WHERE input_token=?`, token).Scan(&state))
		return state == "removed"
	})
	var policy, disposition string
	require(t, db.QueryRow(`SELECT queue_policy,disposition FROM turns WHERE id=?`, sourceID).Scan(&policy, &disposition))
	if policy != "held" || disposition != "pending" {
		t.Fatal("unconsumed Steer was not restored held", policy, disposition)
	}
	second := a.request("POST", base+"/turns", map[string]string{"request_id": "after-stop", "content": "ordinary next run"}, 201)
	secondID := second["id"].(string)
	a.await(20*time.Second, "next normal prompt completion", func() bool {
		var count int
		require(t, db.QueryRow(`SELECT count(*) FROM agent_commands WHERE turn_id=? AND kind='prompt' AND state='completed'`, secondID).Scan(&count))
		return count == 1
	})
	var leaked, userMessages int
	require(t, db.QueryRow(`SELECT count(*) FROM agent_records WHERE kind='native' AND json_extract(payload,'$.type')='input_consumed' AND json_extract(payload,'$.input_token')=?`, token).Scan(&leaked))
	require(t, db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=? AND role='user'`, sourceID).Scan(&userMessages))
	if leaked != 0 || userMessages != 0 {
		t.Fatal("removed Steer leaked into the next run", leaked, userMessages)
	}
	p := a.request("GET", "/api/projects/"+project, nil, 200)
	a.request("DELETE", fmt.Sprintf("/api/projects/%s?revision=%.0f", project, p["revision"]), nil, 202)
	a.await(15*time.Second, "stopped Steer fixture cleanup", func() bool {
		return a.cli("diagnostics", "--socket", filepath.Join(a.root, "state/control.sock"))["state"].(map[string]any)["occupied_projects"] == float64(0)
	})
	t.Log("accepted Steer was removed after Stop; next normal run did not consume it")
}
