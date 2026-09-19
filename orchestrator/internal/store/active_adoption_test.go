package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func activeAdoptionFixture(t *testing.T) (*Store, BoundBridgeStore, execution.Binding, TurnGrant) {
	t.Helper()
	ctx := context.Background()
	st, bridge, b, _ := grantFixture(t)
	if err := st.ActivateExecutionRoutes(ctx, b.SessionID, b.GenerationID, b.Owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	turn := claimGrantTurn(t, st, bridge, b, "original-claim")
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	b, err := st.GetExecutionBinding(ctx, b.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, b.StoreID, b.SessionID, b.GenerationID, b.ProcessHandle, b.Epoch); err != nil {
		t.Fatal(err)
	}
	return st, bridge, b, turn
}

func acceptAdoption(t *testing.T, st *Store, a execution.ExecutionAdoption) {
	t.Helper()
	b := a.Binding
	if err := st.AcceptExecutionBinding(context.Background(), execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: b.Owner, ProcessHandle: b.ProcessHandle, EnvironmentFence: b.EnvironmentFence, Epoch: b.Epoch, ReconciledEventSequence: &a.EventSequence}); err != nil {
		t.Fatal(err)
	}
}

func TestActiveAdoptionTransfersOriginalAttemptButFencesDispatchAndOldGrants(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, turn := activeAdoptionFixture(t)
	original, err := st.AuthorizeEgressRequest(ctx, queryRequest(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='replacement'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE runtime_generations SET lease_expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE turns SET lease_expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	owner := GenerationLeaseOwner("replacement")
	a, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, owner, time.Minute)
	if err != nil || len(a.Grants) != 1 || a.Grants[0] != original.GrantID {
		t.Fatalf("active adoption/drain obligation: %+v %v", a, err)
	}
	var attempt int
	var currentOwner, status string
	if err := st.db.QueryRow(`SELECT attempt,lease_owner,status FROM turns WHERE id=?`, turn.TurnID).Scan(&attempt, &currentOwner, &status); err != nil || attempt != turn.Attempt || currentOwner != owner || status != "running" {
		t.Fatalf("adoption changed original work: %d %s %s %v", attempt, currentOwner, status, err)
	}
	if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: owner, RequestID: "original-claim", LeaseTTL: time.Minute}); err == nil {
		t.Fatal("active replay sent a grant before acceptance")
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("adoption retained gateway authority: %v", err)
	}
	sequence := int64(1)
	output := AppendEventParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: &turn.TurnID, OutputSequence: &sequence, Stream: "stdout", Type: "emit_output", Payload: map[string]string{"line": "original output"}}
	if _, err := st.AppendEvent(ctx, output); err == nil {
		t.Fatal("old owner ingested output after transfer")
	}
	output.Owner = owner
	if _, err := st.AppendEvent(ctx, output); err != nil {
		t.Fatalf("current owner could not recover original output: %v", err)
	}
	if err := st.RenewExecutionBindingLease(ctx, b.GenerationID, owner, 2*time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	var generationExpiry, turnExpiry string
	if err := st.db.QueryRow(`SELECT g.lease_expires_at,t.lease_expires_at FROM runtime_generations g JOIN turns t ON t.generation_id=g.generation_id WHERE t.id=?`, turn.TurnID).Scan(&generationExpiry, &turnExpiry); err != nil || generationExpiry != turnExpiry {
		t.Fatalf("adoption did not renew both leases: %s %s %v", generationExpiry, turnExpiry, err)
	}
	invalidateExecution(t, st, b)
	p := execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: owner, ProcessHandle: b.ProcessHandle, Epoch: b.Epoch, EnvironmentFence: b.EnvironmentFence, ReconciledEventSequence: &a.EventSequence}
	if err := st.AcceptExecutionBinding(ctx, p); err == nil {
		t.Fatal("acceptance cleared a later engine event")
	}
	a, err = st.BeginActiveExecutionAdoption(ctx, b.GenerationID, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := st.db.QueryRow(`SELECT revision FROM active_execution_adoptions`).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("retry replaced grant revision: %d %v", revision, err)
	}
	acceptAdoption(t, st, a)
	request := queryRequest(b)
	request.RequestID = "after-adoption"
	fresh, err := st.AuthorizeEgressRequest(ctx, request)
	if err != nil || fresh.GrantID == original.GrantID || fresh.TurnID != original.TurnID || fresh.Epoch != original.Epoch {
		t.Fatalf("fresh grant lost original execution/turn: %+v %v", fresh, err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrRequestConflict) {
		t.Fatalf("prior gateway request moved into a new grant: %v", err)
	}
	if err := st.db.QueryRow(`SELECT state FROM egress_grants WHERE grant_id=?`, original.GrantID).Scan(&status); err != nil || status != "revoked" {
		t.Fatalf("original grant revived: %s %v", status, err)
	}
	a, err = st.BeginActiveExecutionAdoption(ctx, b.GenerationID, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT revision FROM active_execution_adoptions`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("new adoption reused old grant authority: %d %v", revision, err)
	}
}

func TestActiveReplayCanCompleteBeforeAcceptanceWithoutRedispatch(t *testing.T) {
	ctx := context.Background()
	st, bridge, b, turn := activeAdoptionFixture(t)
	a, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"output_sequence":1,"stream":"stdout","payload":{"line":"finished while detached"}}`)
	if handled, err := st.VerifyActiveExecutionReplay(ctx, a.Binding, "emit_output", &turn.TurnID, payload); err != nil || handled {
		t.Fatalf("live original record bypassed normal ingestion: %t %v", handled, err)
	}
	sequence := int64(1)
	if _, err := st.AppendEvent(ctx, AppendEventParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: &turn.TurnID, OutputSequence: &sequence, Type: "emit_output", Stream: "stdout", Payload: map[string]string{"line": "finished while detached"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID, TerminalStatus: "completed", FinalOutputSequence: &sequence}); err != nil {
		t.Fatal(err)
	}
	if handled, err := st.VerifyActiveExecutionReplay(ctx, a.Binding, "emit_output", &turn.TurnID, payload); err != nil || !handled {
		t.Fatalf("terminal original replay lost exact record: %t %v", handled, err)
	}
	if _, err := st.VerifyActiveExecutionReplay(ctx, a.Binding, "emit_output", &turn.TurnID, json.RawMessage(`{"output_sequence":2,"payload":{}}`)); err == nil {
		t.Fatal("uncommitted late output bypassed historical recovery")
	}
	a, err = st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute)
	if err != nil {
		t.Fatalf("finished turn made adoption unretryable: %v", err)
	}
	acceptAdoption(t, st, a)
	var status string
	if err := st.db.QueryRow(`SELECT status FROM runtime_generations WHERE generation_id=?`, b.GenerationID).Scan(&status); err != nil || status != "idle" {
		t.Fatalf("finished adoption reactivated a terminal turn: %s %v", status, err)
	}
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("terminal adoption issued grants: %v", err)
	}
}

func TestActiveAdoptionPreservesStopAndOriginalControlOwner(t *testing.T) {
	ctx := context.Background()
	st, _, b, turn := activeAdoptionFixture(t)
	command := execution.TurnCommand{ID: "control_abort_original", SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: b.Owner, TurnID: turn.TurnID, Kind: "abort"}
	if _, err := st.ReserveTurnCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='replacement'`); err != nil {
		t.Fatal(err)
	}
	owner := GenerationLeaseOwner("replacement")
	a, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	acceptAdoption(t, st, a)
	if _, err := st.AuthorizeEgressRequest(ctx, queryRequest(b)); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("reconnect undid Stop: %v", err)
	}
	work, err := st.PendingExecutionWork(ctx, execution.ReconcileAbort, owner, "", 16)
	if err != nil || len(work) != 1 || work[0].Command.Owner != owner || work[0].Command.ID != command.ID {
		t.Fatalf("current owner lost pending original control: %+v %v", work, err)
	}
	if _, err := st.ReserveTurnCommand(ctx, command); err == nil {
		t.Fatal("old owner regained control delivery")
	}
	retry, err := st.ReserveTurnCommand(ctx, work[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AcknowledgeTurnCommand(ctx, a.Binding, command.ID); err != nil {
		t.Fatal(err)
	}
	if ack, err := st.TurnCommandAcknowledged(ctx, retry); err != nil || !ack {
		t.Fatalf("current owner lost original command ACK: %t %v", ack, err)
	}
	var originalOwner string
	if err := st.db.QueryRow(`SELECT owner FROM execution_turn_commands WHERE command_id=?`, command.ID).Scan(&originalOwner); err != nil || originalOwner != b.Owner {
		t.Fatalf("adoption rewrote control provenance: %s %v", originalOwner, err)
	}
}

func TestActiveAdoptionRejectsChangedPlacementAndExpiredReplayLease(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := activeAdoptionFixture(t)
	a, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE turns SET lease_expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if err := st.RenewExecutionBindingLease(ctx, b.GenerationID, b.Owner, time.Minute, time.Now()); !errors.Is(err, execution.ErrStartLeaseLost) {
		t.Fatalf("expired adoption turn silently renewed: %v", err)
	}
	if _, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "pause", EnvironmentID: b.EnvironmentID, Kind: "pause", IdempotencyKey: "pause", ExpectedFence: b.EnvironmentFence, Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute); !errors.Is(err, execution.ErrStaleOperation) {
		t.Fatalf("active retry crossed lifecycle fence: %v", err)
	}
	if _, err := st.CommitEngineEvents(ctx, b.StoreID, 0, []sandbox.Event{{ID: "paused", Sequence: 1, SandboxID: b.SandboxID, Kind: "paused", Epoch: b.Epoch}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: b.Owner, ProcessHandle: b.ProcessHandle, Epoch: b.Epoch, EnvironmentFence: b.EnvironmentFence, ReconciledEventSequence: &a.EventSequence}); err == nil {
		t.Fatal("stale adoption restored readiness")
	}
}

func TestActiveEventInvalidationSelectsRevalidationWithoutOwnerOrLeaseChange(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := activeAdoptionFixture(t)
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileActive, b.Owner, "", 16); err != nil || len(work) != 0 {
		t.Fatalf("healthy owned turn selected for adoption: %+v %v", work, err)
	}
	invalidateExecution(t, st, b)
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileActive, b.Owner, "", 16); err != nil || len(work) != 1 || work[0].GenerationID != b.GenerationID {
		t.Fatalf("event did not select active revalidation: %+v %v", work, err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileIdle, b.Owner, "", 16); err != nil || len(work) != 0 {
		t.Fatalf("active invalidation entered idle adoption: %+v %v", work, err)
	}
}

func TestActiveAdoptionCanEscalateToDurableInterruptedRecovery(t *testing.T) {
	ctx := context.Background()
	st, _, b, turn := activeAdoptionFixture(t)
	if _, err := st.BeginActiveExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute); err != nil {
		t.Fatal(err)
	}
	recovery, err := st.NextInterruptedRecovery(ctx, b.GenerationID, b.Owner, execution.ActiveAttachmentLostReason)
	if err != nil || recovery.Stage != "pause" || recovery.Operation.Fence != b.EnvironmentFence+1 {
		t.Fatalf("failed active attachment recovery: %+v %v", recovery, err)
	}
	if status, _, err := st.TurnStatus(ctx, b.SessionID, turn.TurnID); err != nil || status != "running" {
		t.Fatalf("recovery classified work before retained replay: %s %v", status, err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileActive, b.Owner, "", 16); err != nil || len(work) != 0 {
		t.Fatalf("adoption competed with recovery: %+v %v", work, err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileInterrupted, b.Owner, "", 16); err != nil || len(work) != 1 || work[0].Reason != execution.ActiveAttachmentLostReason {
		t.Fatalf("active recovery not restartable: %+v %v", work, err)
	}
}
