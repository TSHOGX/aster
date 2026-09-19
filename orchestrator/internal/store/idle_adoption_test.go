package store

import (
	"context"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"testing"
	"time"
)

func TestFailedIdleAdoptionEntersDurableInterruptedRecovery(t *testing.T) {
	ctx := context.Background()
	st, _, b, observation := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, b.StoreID, b.SessionID, b.GenerationID, observation.ProcessHandle, b.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NextInterruptedRecovery(ctx, b.GenerationID, b.Owner, execution.IdleAttachmentLostReason); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("ordinary startup entered idle recovery: %v", err)
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginIdleExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute); err != nil {
		t.Fatal(err)
	}
	recovery, err := st.NextInterruptedRecovery(ctx, b.GenerationID, b.Owner, execution.IdleAttachmentLostReason)
	if err != nil || recovery.Stage != "pause" || recovery.Operation.Kind != "pause" || recovery.Operation.Fence != b.EnvironmentFence+1 {
		t.Fatalf("failed adoption recovery: %+v %v", recovery, err)
	}
	if _, err := st.BeginIdleExecutionAdoption(ctx, b.GenerationID, b.Owner, time.Minute); err == nil {
		t.Fatal("adoption crossed reserved recovery lifecycle")
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileIdle, b.Owner, "", 16); err != nil || len(work) != 0 {
		t.Fatalf("recovery duplicated idle adoption work: %+v %v", work, err)
	}
	if work, err := st.PendingExecutionWork(ctx, execution.ReconcileInterrupted, b.Owner, "", 16); err != nil || len(work) != 1 || work[0].Reason != execution.IdleAttachmentLostReason {
		t.Fatalf("recovery obligation not restartable: %+v %v", work, err)
	}
	retry, err := st.NextInterruptedRecovery(ctx, b.GenerationID, b.Owner, execution.IdleAttachmentLostReason)
	if err != nil || retry.Operation.ID != recovery.Operation.ID || retry.Operation.Fence != recovery.Operation.Fence {
		t.Fatalf("recovery retry replaced lifecycle intent: %+v %v", retry, err)
	}
}

func TestIdleAdoptionFencesDispatchUntilCurrentOwnerReplayAcceptance(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, observation := boundExecutionFixture(t)
	if err := st.ObserveExecutionProcess(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE orchestrator_owner SET uuid='new-owner' WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	owner := GenerationLeaseOwner("new-owner")
	adoption, err := st.BeginIdleExecutionAdoption(ctx, binding.GenerationID, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if adoption.Binding.GenerationID != binding.GenerationID || adoption.Binding.Epoch != binding.Epoch || adoption.Binding.Owner != owner || adoption.Binding.State != "starting" {
		t.Fatalf("adoption replaced recorded execution: %+v", adoption)
	}
	if _, err := st.EnqueueTurn(ctx, binding.SessionID, "queued during reconnect", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "too-early", LeaseTTL: time.Minute}); err == nil {
		t.Fatal("adoption admitted work before replay")
	}
	observation.Owner = owner
	observation.ReconciledEventSequence = &adoption.EventSequence
	if err := st.AcceptExecutionBinding(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bridge.ClaimNextTurn(ctx, ClaimNextTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: owner, RequestID: "after-recovery", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatalf("accepted adoption cannot dispatch: %t %v", ok, err)
	}
}

func TestIdleAdoptionRefusesInFlightTurnAndNewerInvalidation(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	if _, err := st.BeginIdleExecutionAdoption(ctx, binding.GenerationID, binding.Owner, time.Minute); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("idle adoption silently adopted live turn: %v", err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, TurnID: turn.TurnID, Owner: binding.Owner, TerminalStatus: "canceled"}); err != nil {
		t.Fatal(err)
	}
	adoption, err := st.BeginIdleExecutionAdoption(ctx, binding.GenerationID, binding.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitEngineEvents(ctx, binding.StoreID, 0, []sandbox.Event{{ID: "after-inspection", Sequence: 1, SandboxID: binding.SandboxID, Kind: "paused"}}); err != nil {
		t.Fatal(err)
	}
	b := adoption.Binding
	if err := st.AcceptExecutionBinding(ctx, execution.ObserveBindingParams{GenerationID: b.GenerationID, Owner: b.Owner, ProcessHandle: b.ProcessHandle, Epoch: b.Epoch, EnvironmentFence: b.EnvironmentFence, ReconciledEventSequence: &adoption.EventSequence}); err == nil {
		t.Fatal("adoption cleared event newer than its evidence")
	}
}

func TestIdleReplayOnlyAcceptsAlreadyCommittedTerminalOutput(t *testing.T) {
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	turn := claimGrantTurn(t, st, bridge, binding, "claim")
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	sequence := int64(1)
	payload := map[string]string{"line": "committed"}
	if _, err := st.AppendEvent(ctx, AppendEventParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: &turn.TurnID, Type: "emit_output", OutputSequence: &sequence, Stream: "stdout", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CompleteTurn(ctx, CompleteTurnParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, TerminalStatus: "canceled"}); err != nil {
		t.Fatal(err)
	}
	records, err := st.PendingBridgeExecutionRecords(ctx, binding.GenerationID, 128)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginIdleExecutionAdoption(ctx, binding.GenerationID, binding.Owner, time.Minute); !errors.Is(err, execution.ErrBusy) {
		t.Fatalf("adoption bypassed pending projection: %v", err)
	}
	for _, record := range records {
		previous, err := st.LoadBridgeProjection(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CommitBridgeProjection(ctx, record, previous.LastRecordID, json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	adoption, err := st.BeginIdleExecutionAdoption(ctx, binding.GenerationID, binding.Owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyIdleExecutionReplay(ctx, adoption.Binding, "emit_output", &turn.TurnID, json.RawMessage(`{"output_sequence":1,"stream":"stdout","payload":{"line":"committed"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyIdleExecutionReplay(ctx, adoption.Binding, "emit_output", &turn.TurnID, json.RawMessage(`{"output_sequence":1,"stream":"stdout","payload":{"line":"changed"}}`)); err == nil {
		t.Fatal("contradictory idle replay accepted")
	}
	if err := st.VerifyIdleExecutionReplay(ctx, adoption.Binding, "emit_output", &turn.TurnID, json.RawMessage(`{"output_sequence":2,"stream":"stdout","payload":{"line":"new"}}`)); err == nil {
		t.Fatal("idle replay ingested unknown late output")
	}
}
