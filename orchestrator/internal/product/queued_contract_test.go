package product

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

func ingestContract(t *testing.T, s *Store, i Incarnation, c agentwire.Frame, kind, body string) {
	t.Helper()
	var seq uint64
	must(t, s.reader.QueryRow(`SELECT ingested_sequence FROM agent_incarnations WHERE id=?`, i.ID).Scan(&seq))
	_, err := s.IngestAgent(ctx, i, record(i, c, seq+1, kind, body))
	must(t, err)
}
func targetControl(i Incarnation, c agentwire.Frame, t Turn, request string) ExecutionControl {
	return ExecutionControl{RequestID: request, Action: "steer", Source: t.ID, PromptRevision: t.PromptRevision, RunID: c.CommandID, IncarnationID: i.ID, Owner: i.Owner}
}
func stopControl(i Incarnation, c agentwire.Frame, request string) ExecutionControl {
	return ExecutionControl{RequestID: request, Action: "stop", RunID: c.CommandID, IncarnationID: i.ID, Owner: i.Owner}
}
func controlErr(s *Store, scope Scope, session string, req ExecutionControl) error {
	_, err := s.ControlRun(ctx, scope, session, req)
	return err
}
func mutationErr(s *Store, scope Scope, session string, prompt Turn, request, action string) error {
	_, err := s.MutatePrompt(ctx, scope, session, prompt.ID, request, action, prompt.PromptRevision)
	return err
}
func completeContract(t *testing.T, s *Store, i Incarnation, c agentwire.Frame) {
	t.Helper()
	ingestContract(t, s, i, c, "command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/%s/native/captures/%s.jsonl","digest":"sha256:%064d","size":0,"outcome":"completed"}`, i.SessionID, c.CommandID, 0))
}
func stateOf(t *testing.T, s *Store, table, id string) string {
	t.Helper()
	var state string
	must(t, s.reader.QueryRow(`SELECT state FROM `+table+` WHERE id=?`, id).Scan(&state))
	return state
}

func TestInputClaimConsumptionAndCommandFactsAreIndependent(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "command.applied", `{}`)
	messages, err := s.Messages(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if len(messages) != 0 {
		t.Fatal("ACK created transcript input")
	}
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	var disposition string
	must(t, s.reader.QueryRow(`SELECT disposition FROM turns WHERE id=?`, c.TurnID).Scan(&disposition))
	if disposition != "consumed" || stateOf(t, s, "execution_runs", c.CommandID) != "running" {
		t.Fatal("consumption completed execution")
	}
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "guidance", "identical", false)
	must(t, err)
	req := targetControl(i, c, source, "steer")
	first, err := s.ControlRun(ctx, scope, i.SessionID, req)
	must(t, err)
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	var native map[string]string
	must(t, json.Unmarshal(steer.Payload, &native))
	ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:02Z","run_elapsed_ms":2000}`, native["input_token"], c.CommandID))
	ingestContract(t, s, i, steer, "command.unknown", `{}`)
	ingestContract(t, s, i, steer, "command.applied", `{}`)
	if stateOf(t, s, "input_deliveries", first.DeliveryID) != "consumed" {
		t.Fatal("transport evidence regressed consumption")
	}
	ingestContract(t, s, i, c, "native", reconciledPayload(c))
	completeContract(t, s, i, c)
	_, err = s.BeginReleaseDrain(ctx, "retry-after-drain", time.Minute)
	must(t, err)
	retry, err := s.ControlRun(ctx, scope, i.SessionID, req)
	must(t, err)
	if retry != first {
		t.Fatal("retry changed original receipt")
	}
	req.RunID = "another-run"
	_, err = s.ControlRun(ctx, scope, i.SessionID, req)
	wantError(t, err, ErrConflict)
}

func TestConcurrentSteerAndPolicyClaimsHaveSingleWinner(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "race-source", "race", false)
	must(t, err)
	start := make(chan struct{})
	results := make(chan error, 16)
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			if n%2 == 0 {
				_, err := s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, fmt.Sprint("race-", n)))
				results <- err
			} else {
				_, err := s.MutatePrompt(ctx, scope, i.SessionID, source.ID, fmt.Sprint("race-", n), "cancel", source.PromptRevision)
				results <- err
			}
		}(n)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d mutations won same source revision", wins)
	}
	var claims int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM input_deliveries WHERE prompt_id=?`, source.ID).Scan(&claims))
	if claims > 1 {
		t.Fatal("multiple claims")
	}
	// Even a path bypassing the API cannot create a second live delivery claim.
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, _, err := createDelivery(ctx, tx, scope.ProjectID, i.SessionID, c.TurnID, "duplicate", c.CommandID, c.CommandID, "normal", s.timestamp())
		return err
	})
	if err == nil {
		t.Fatal("normal/steer shared claim could be bypassed")
	}
}

func TestLateSteerReconciliationDoesNotBlockJournalOrResubmit(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint("dispatched=", dispatched), func(t *testing.T) {
			s, scope, i, c := agentFixture(t)
			ingestContract(t, s, i, c, "native", consumedPayload(c))
			source, err := s.AdmitTurn(ctx, scope, i.SessionID, "late-source", "late", false)
			must(t, err)
			accepted, err := s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, "late"))
			must(t, err)
			var steer agentwire.Frame
			if dispatched {
				steer, err = s.agentCommand(ctx, i)
				must(t, err)
			}
			ingestContract(t, s, i, c, "native", reconciledPayload(c))
			completeContract(t, s, i, c)
			want := "settled"
			if dispatched {
				want = "unknown"
			}
			if got := stateOf(t, s, "execution_runs", c.CommandID); got != want {
				t.Fatalf("run %s want %s", got, want)
			}
			if dispatched {
				ingestContract(t, s, i, steer, "command.rejected", `{"error":"target ended"}`)
			}
			if got := stateOf(t, s, "execution_runs", c.CommandID); got != "settled" {
				t.Fatal("barrier stranded run", got)
			}
			var policy, disposition string
			must(t, s.reader.QueryRow(`SELECT queue_policy,disposition FROM turns WHERE id=?`, source.ID).Scan(&policy, &disposition))
			if policy != "held" || disposition != "pending" {
				t.Fatal(policy, disposition)
			}
			if got := stateOf(t, s, "input_deliveries", accepted.DeliveryID); got != "removed" && got != "rejected" {
				t.Fatal(got)
			}
			_, err = s.agentCommand(ctx, i)
			wantError(t, err, ErrNotFound)
		})
	}
}

func TestRunRejectionAndCrashKeepEvidenceRecoverable(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "command.rejected", `{"error":"preflight"}`)
	if stateOf(t, s, "execution_runs", c.CommandID) != "failed" {
		t.Fatal("rejection occupied run")
	}
	turns, err := s.Turns(ctx, scope, i.SessionID, 0, 10)
	must(t, err)
	if turns[0].Disposition != "pending" || turns[0].QueuePolicy != "held" {
		t.Fatal(turns)
	}
	_, err = s.MutatePrompt(ctx, scope, i.SessionID, c.TurnID, "retry-release", "release", turns[0].PromptRevision)
	must(t, err)
	next, err := s.agentCommand(ctx, i)
	must(t, err)
	if next.CommandID == c.CommandID {
		t.Fatal("new explicit attempt reused rejected command")
	}
	ingestContract(t, s, i, next, "native", consumedPayload(next))
	must(t, s.write(ctx, func(tx *sql.Tx) error { return markRunUnknown(ctx, tx, i.SessionID) }))
	var state string
	must(t, s.reader.QueryRow(`SELECT state FROM input_deliveries WHERE command_id=?`, next.CommandID).Scan(&state))
	if state != "consumed" {
		t.Fatal("crash erased consumption")
	}
	_, err = s.agentCommand(ctx, i)
	wantError(t, err, ErrPending)
}

func TestSegmentsCoalescePersistAndRebuildWithIndependentClocks(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	steer := func(name string, elapsed int) {
		t.Helper()
		source, err := s.AdmitTurn(ctx, scope, i.SessionID, name, name, true)
		must(t, err)
		_, err = s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, name+"-steer"))
		must(t, err)
		command, err := s.agentCommand(ctx, i)
		must(t, err)
		var native map[string]string
		must(t, json.Unmarshal(command.Payload, &native))
		ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"input_consumed","input_token":%q,"run_id":%q,"occurred_at":"2026-09-24T00:00:0%dZ","run_elapsed_ms":%d}`, native["input_token"], c.CommandID, elapsed/1000, elapsed))
	}
	steer("initial guidance", 2000)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	ingestContract(t, s, i, c, "native", `{"type":"message_start","message":{"role":"assistant","content":[{"type":"toolCall","id":"tool","name":"read"}]}}`)
	ingestContract(t, s, i, c, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"toolCall","id":"tool","name":"read"}]}}`)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	steer("later guidance", 3000)
	must(t, s.ProjectConversation(ctx, i.SessionID))
	ingestContract(t, s, i, c, "native", `{"type":"tool_execution_end","toolCallId":"tool","result":{"content":[{"type":"text","text":"late update"}]}}`)
	ingestContract(t, s, i, c, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"last"}]}}`)
	ingestContract(t, s, i, c, "native", reconciledPayload(c))
	completeContract(t, s, i, c)
	for n := 0; n < 4; n++ {
		must(t, s.ProjectConversation(ctx, i.SessionID))
	}
	before := displayItems(t, s, i.SessionID)
	if len(before) != 6 {
		t.Fatalf("items: %+v", before)
	}
	if before[0].SegmentID != before[1].SegmentID || before[2].SegmentID != before[0].SegmentID || before[3].SegmentID != before[0].SegmentID || before[4].SegmentID != before[5].SegmentID || before[0].SegmentID == before[5].SegmentID {
		t.Fatalf("segment ownership: %+v", before)
	}
	var firstElapsed, lastElapsed int64
	var firstState string
	must(t, s.reader.QueryRow(`SELECT elapsed_ms,state FROM conversation_segments WHERE session_id=? ORDER BY position LIMIT 1`, i.SessionID).Scan(&firstElapsed, &firstState))
	must(t, s.reader.QueryRow(`SELECT elapsed_ms FROM conversation_segments WHERE session_id=? ORDER BY position DESC LIMIT 1`, i.SessionID).Scan(&lastElapsed))
	if firstElapsed != 2000 || lastElapsed != 1000 || firstState != "continued" {
		t.Fatal(firstElapsed, lastElapsed, firstState)
	}
	must(t, s.RebuildConversation(ctx, scope, i.SessionID))
	for n := 0; n < 4; n++ {
		must(t, s.ProjectConversation(ctx, i.SessionID))
	}
	after := displayItems(t, s, i.SessionID)
	for n := range before {
		before[n].Revision = 0
		after[n].Revision = 0
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild differs\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestRejectedNormalRunWaitsForInFlightSteerDisposition(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "preflight-guidance", "guidance", true)
	must(t, err)
	_, err = s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, "preflight-steer"))
	must(t, err)
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	ingestContract(t, s, i, c, "command.rejected", `{"error":"prompt preflight rejected"}`)
	if stateOf(t, s, "execution_runs", c.CommandID) != "unknown" {
		t.Fatal("released run before control disposition")
	}
	ingestContract(t, s, i, steer, "command.rejected", `{"error":"target ended"}`)
	if stateOf(t, s, "execution_runs", c.CommandID) != "failed" {
		t.Fatal("late rejection stranded failed run")
	}
}

func TestRemovedNormalInputCanBeExplicitlyRetriedWithoutCaptureCollision(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	var token string
	must(t, s.reader.QueryRow(`SELECT input_token FROM input_deliveries WHERE command_id=?`, c.CommandID).Scan(&token))
	ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"inputs_reconciled","run_id":%q,"removed_input_tokens":[%q],"complete":true,"occurred_at":"2026-09-24T00:00:04Z","run_elapsed_ms":4000}`, c.CommandID, token))
	completeContract(t, s, i, c)
	turns, err := s.Turns(ctx, scope, i.SessionID, 0, 10)
	must(t, err)
	_, err = s.MutatePrompt(ctx, scope, i.SessionID, c.TurnID, "explicit-retry", "release", turns[0].PromptRevision)
	must(t, err)
	next, err := s.agentCommand(ctx, i)
	must(t, err)
	ingestContract(t, s, i, next, "native", consumedPayload(next))
	ingestContract(t, s, i, next, "native", reconciledPayload(next))
	completeContract(t, s, i, next)
	var captures int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM native_captures WHERE turn_id=?`, c.TurnID).Scan(&captures))
	if captures != 2 {
		t.Fatal("retry lost per-run capture", captures)
	}
}

func TestLifecycleAndSegmentTablesParticipateInProjectPurge(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	must(t, s.ProjectConversation(ctx, i.SessionID))
	must(t, s.write(ctx, func(tx *sql.Tx) error { return s.purgeProject(ctx, tx, scope.ProjectID) }))
	for _, table := range []string{"execution_runs", "input_deliveries", "control_receipts", "conversation_segments", "conversation_segment_inputs"} {
		var count int
		must(t, s.reader.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		if count != 0 {
			t.Fatal("purge retained lifecycle row", table, count)
		}
	}
}

func TestServiceAttributionCannotMoveToRetryOfSamePrompt(t *testing.T) {
	s, scope, i, c := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	original, err := authorizeService(ctx, s.reader, s.timestamp(), "model", token)
	must(t, err)
	ingestContract(t, s, i, c, "command.rejected", `{"error":"preflight rejected"}`)
	turns, err := s.Turns(ctx, scope, i.SessionID, 0, 10)
	must(t, err)
	_, err = s.MutatePrompt(ctx, scope, i.SessionID, c.TurnID, "new-attempt", "release", turns[0].PromptRevision)
	must(t, err)
	next, err := s.agentCommand(ctx, i)
	must(t, err)
	replacement, err := authorizeService(ctx, s.reader, s.timestamp(), "model", token)
	must(t, err)
	if replacement.RunID != next.CommandID || replacement.TurnID != original.TurnID {
		t.Fatal("incorrect retry attribution")
	}
	wantError(t, holdsAttribution(ctx, s.reader, s.timestamp(), original), ErrNotFound)
	must(t, holdsAttribution(ctx, s.reader, s.timestamp(), replacement))
}

func TestAckResolvesAcceptanceUncertaintyWithoutUndoingRemoval(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "ack-source", "guidance", true)
	must(t, err)
	receipt, err := s.ControlRun(ctx, scope, i.SessionID, targetControl(i, c, source, "ack-guidance"))
	must(t, err)
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	ingestContract(t, s, i, steer, "command.unknown", `{}`)
	ingestContract(t, s, i, steer, "command.applied", `{}`)
	if stateOf(t, s, "input_deliveries", receipt.DeliveryID) != "native_queued" {
		t.Fatal("ACK did not resolve acceptance uncertainty")
	}
	var token string
	must(t, s.reader.QueryRow(`SELECT input_token FROM input_deliveries WHERE id=?`, receipt.DeliveryID).Scan(&token))
	ingestContract(t, s, i, c, "native", fmt.Sprintf(`{"type":"inputs_reconciled","run_id":%q,"removed_input_tokens":[%q],"complete":true,"occurred_at":"2026-09-24T00:00:04Z","run_elapsed_ms":4000}`, c.CommandID, token))
	ingestContract(t, s, i, steer, "command.applied", `{}`)
	if stateOf(t, s, "input_deliveries", receipt.DeliveryID) != "removed" {
		t.Fatal("late ACK resurrected removed input")
	}
}

func TestStopCanQuiesceExactUnknownRunWhileSessionIsBlocked(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "command.unknown", `{"error":"uncertain"}`)
	req := ExecutionControl{RequestID: "stop-unknown", Action: "stop", RunID: c.CommandID, IncarnationID: i.ID, Owner: i.Owner}
	accepted, err := s.ControlRun(ctx, scope, i.SessionID, req)
	must(t, err)
	// The Stop is urgent even if recovery still marks the Session blocked.
	_, err = s.db.Exec(`UPDATE sessions SET state='blocked' WHERE id=?`, i.SessionID)
	must(t, err)
	stop, err := s.agentCommand(ctx, i)
	must(t, err)
	if stop.CommandID != accepted.CommandID {
		t.Fatal("Stop was not dispatched")
	}
	var payload map[string]string
	must(t, json.Unmarshal(stop.Payload, &payload))
	if payload["type"] != "abort" || payload["run_id"] != c.CommandID {
		t.Fatal("Stop retargeted", payload)
	}
}
