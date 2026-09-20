package store

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
	"testing"
	"time"
)

func steeringFixture(t *testing.T) (*Store, *Store, execution.Binding, execution.TurnCommand, int64) {
	t.Helper()
	ctx := context.Background()
	st, bridge, binding, observed := boundExecutionFixtureForDriver(t, "pi")
	if err := st.ObserveExecutionProcess(ctx, observed); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observed); err != nil {
		t.Fatal(err)
	}
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	queued, err := st.EnqueueTurn(ctx, binding.SessionID, "steer once", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	command, err := st.RequestBoundSteer(ctx, binding.SessionID, binding.GenerationID, binding.Owner, queued, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	return st, bridge, binding, command, queued
}

func TestHistoricalSteeringResultRetainsOriginalPromptWithoutDispatch(t *testing.T) {
	ctx := context.Background()
	st, _, binding, command, queued := steeringFixture(t)
	binding, err := st.GetExecutionBinding(ctx, binding.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, binding.ProcessHandle, binding.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailGenerationWithEvent(ctx, FailGenerationParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: command.TurnID, ErrorClass: "placement_lost", Reason: "lost reply"}); err != nil {
		t.Fatal(err)
	}
	resume, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "resume", EnvironmentID: binding.EnvironmentID, Kind: "resume", IdempotencyKey: "resume", ExpectedFence: binding.EnvironmentFence})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: resume.ID, Fence: resume.Fence, State: execution.OperationSucceeded, SandboxID: binding.SandboxID, Epoch: binding.Epoch + 1, MutationRevision: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(execution.JournalRecoveryRequest{GenerationID: binding.GenerationID, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch + 1, Handle: "reader"}}})
	op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "recover", EnvironmentID: binding.EnvironmentID, Kind: "recover_journal", IdempotencyKey: "recover", ExpectedFence: resume.Fence, Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationIssued}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordJournalRecoveryHead(ctx, op.ID, op.Fence, binding.Owner, 1); err != nil {
		t.Fatal(err)
	}
	outcome, _ := json.Marshal(execution.SteeringOutcome{CommandID: command.ID, Status: "delivered"})
	record := execution.RecoveredJournalRecord{Sequence: 1, Digest: strings.Repeat("a", 64), SessionID: binding.SessionID, GenerationID: binding.GenerationID, MessageID: "steering-result", Type: "turn_control_result", TurnID: &command.TurnID, Payload: outcome}
	for range 2 {
		if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, binding.Owner, record); err != nil {
			t.Fatal(err)
		}
	}
	status, _, err := st.TurnStatus(ctx, binding.SessionID, command.TurnID)
	if err != nil || status != "failed" {
		t.Fatal("historical steering changed failed destination turn")
	}
	status, _, err = st.TurnStatus(ctx, binding.SessionID, queued)
	if err != nil || status != "completed" {
		t.Fatal("historical steering lost source prompt result")
	}
	var messages int
	if err := st.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=?`, queued).Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("historical steering duplicated transcript: %d %v", messages, err)
	}
	if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, binding.Owner, time.Now()); err == nil {
		t.Fatal("historical steering restored execution authority")
	}
}
func TestSteeringOutcomeCommitsMessageOnceAfterTargetHasEnded(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, command, queued := steeringFixture(t)
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: command.TurnID, TerminalStatus: "canceled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{SessionID: binding.SessionID, TurnID: queued}); err == nil {
		t.Fatal("late outcome let source be claimed")
	}
	var event int64
	for i := 0; i < 2; i++ {
		id, err := st.RecordSteeringOutcome(ctx, binding.SessionID, binding.GenerationID, binding.Owner, command.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "delivered"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			event = id
		} else if event != id {
			t.Fatal("outcome retry allocated another event")
		}
	}
	var messages int
	if err := st.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=?`, queued).Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("steering message count: %d %v", messages, err)
	}
	status, _, err := st.TurnStatus(ctx, binding.SessionID, queued)
	if err != nil || status != "completed" {
		t.Fatalf("delivered source outcome: %s %v", status, err)
	}
	if _, err := st.RecordSteeringOutcome(ctx, binding.SessionID, binding.GenerationID, binding.Owner, command.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "rejected"}); err == nil {
		t.Fatal("contradictory guest outcome accepted")
	}
}
func TestSteeringRejectionPreservesParkedPromptForExplicitRelease(t *testing.T) {
	ctx := context.Background()
	st, _, binding, command, queued := steeringFixture(t)
	before, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
	if err != nil || len(before.QueuedTurns) != 1 || !before.QueuedTurns[0].SteeringPending {
		t.Fatalf("pending delivery hidden: %+v %v", before, err)
	}
	if _, err := st.RecordSteeringOutcome(ctx, binding.SessionID, binding.GenerationID, binding.Owner, command.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "rejected"}); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetSessionRuntimeSnapshot(ctx, binding.SessionID)
	if err != nil || len(after.QueuedTurns) != 1 || after.QueuedTurns[0].SteeringPending || after.Revision <= before.Revision {
		t.Fatalf("rejected delivery did not advance runtime: %+v %v", after, err)
	}
	status, _, err := st.TurnStatus(ctx, binding.SessionID, queued)
	if err != nil || status != "queued" {
		t.Fatalf("rejected steering dropped prompt: %s %v", status, err)
	}
	var active, messages int
	if err := st.db.QueryRow(`SELECT auto_dispatch FROM turns WHERE id=?`, queued).Scan(&active); err != nil || active != 0 {
		t.Fatal("rejected steering automatically redispatched")
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=?`, queued).Scan(&messages); err != nil || messages != 0 {
		t.Fatal("rejected steering invented transcript")
	}
	if _, err := st.ReleaseParkedTurn(ctx, ReleaseParkedTurnParams{SessionID: binding.SessionID, TurnID: queued}); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSteeringOutcomeRemainsUnknownWithoutImplicitReplay(t *testing.T) {
	ctx := context.Background()
	st, _, binding, command, queued := steeringFixture(t)
	if _, err := st.RecordSteeringOutcome(ctx, binding.SessionID, binding.GenerationID, binding.Owner, command.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "unknown"}); err == nil {
		t.Fatal("guest chose host recovery classification")
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := recordSteeringOutcomeTx(ctx, tx, binding.SessionID, binding.GenerationID, command.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, _, err := st.TurnStatus(ctx, binding.SessionID, queued)
	if err != nil || status != "failed" {
		t.Fatalf("unknown steering was requeued: %s %v", status, err)
	}
	var messages int
	if err := st.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=?`, queued).Scan(&messages); err != nil || messages != 0 {
		t.Fatal("unknown steering invented delivered transcript")
	}
}
func TestSteeringOutcomeAndTranscriptRollbackTogether(t *testing.T) {
	ctx := context.Background()
	st, _, binding, command, queued := steeringFixture(t)
	if _, err := st.db.Exec(`CREATE TRIGGER reject_steering_event BEFORE INSERT ON events WHEN NEW.type='message.created' BEGIN SELECT RAISE(ABORT,'injected event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSteeringOutcome(ctx, binding.SessionID, binding.GenerationID, binding.Owner, command.TurnID, execution.SteeringOutcome{CommandID: command.ID, Status: "delivered"}); err == nil {
		t.Fatal("event failure ignored")
	}
	var outcome string
	if err := st.db.QueryRow(`SELECT outcome FROM execution_steering WHERE command_id=?`, command.ID).Scan(&outcome); err != nil || outcome != "pending" {
		t.Fatalf("event failure consumed outcome: %s %v", outcome, err)
	}
	var messages int
	if err := st.db.QueryRow(`SELECT count(*) FROM messages WHERE turn_id=?`, queued).Scan(&messages); err != nil || messages != 0 {
		t.Fatal("event failure partially published message")
	}
}
