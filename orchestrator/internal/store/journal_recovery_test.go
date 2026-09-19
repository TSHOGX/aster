package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func recoveryFixture(t *testing.T) (*Store, execution.Binding, execution.Operation, int64) {
	t.Helper()
	ctx := context.Background()
	st, bridge, binding, _ := grantFixture(t)
	stored, err := st.GetExecutionBinding(ctx, binding.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	binding = stored
	turn := claimGrantTurn(t, st, bridge, binding, "before-loss")
	if _, err := bridge.AckTurnStarted(ctx, AckStartedParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindBridgeStream(ctx, binding.StoreID, binding.SessionID, binding.GenerationID, binding.ProcessHandle, binding.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailGenerationWithEvent(ctx, FailGenerationParams{SessionID: binding.SessionID, GenerationID: binding.GenerationID, Owner: binding.Owner, TurnID: turn.TurnID, ErrorClass: "placement_lost", Reason: "synthetic placement loss"}); err != nil {
		t.Fatal(err)
	}
	op, err := st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "maintenance-resume", EnvironmentID: binding.EnvironmentID, Kind: "resume", IdempotencyKey: "maintenance-resume", ExpectedFence: binding.EnvironmentFence})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, SandboxID: binding.SandboxID, Epoch: binding.Epoch + 1, MutationRevision: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	request := execution.JournalRecoveryRequest{GenerationID: binding.GenerationID, Process: sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: binding.StoreID, SandboxID: binding.SandboxID, Epoch: binding.Epoch + 1, Handle: "historical-reader"}}}
	raw, _ := json.Marshal(request)
	op, err = st.BeginEnvironmentOperation(ctx, execution.OperationIntent{ID: "recovery", EnvironmentID: binding.EnvironmentID, Kind: "recover_journal", IdempotencyKey: "recovery", ExpectedFence: op.Fence, Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationIssued}); err != nil {
		t.Fatal(err)
	}
	return st, binding, op, turn.TurnID
}

func historicalRecord(binding execution.Binding, turn int64, sequence uint64, kind, payload string) execution.RecoveredJournalRecord {
	return execution.RecoveredJournalRecord{Sequence: sequence, Digest: strings.Repeat(string(rune('a'+sequence)), 64), SessionID: binding.SessionID, GenerationID: binding.GenerationID, MessageID: "original-message", TurnID: &turn, Type: kind, Payload: json.RawMessage(payload)}
}

func TestHistoricalRecoveryPreservesFailedTurnAndNeverRestoresAuthority(t *testing.T) {
	ctx := context.Background()
	st, binding, op, turn := recoveryFixture(t)
	if err := st.RecordJournalRecoveryHead(ctx, op.ID, op.Fence, binding.Owner, 3); err != nil {
		t.Fatal(err)
	}
	records := []execution.RecoveredJournalRecord{
		historicalRecord(binding, turn, 1, "claim_next_turn", `{}`),
		historicalRecord(binding, turn, 2, "emit_output", `{"output_sequence":1,"stream":"stdout","payload":{"line":"retained historical output","integer":9007199254740993}}`),
		historicalRecord(binding, turn, 3, "ack_turn_completed", `{"status":"completed","final_output_sequence":1,"driver_state_update":{"untrusted":"never apply"}}`),
	}
	for _, record := range records {
		if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, binding.Owner, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range records {
		if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, binding.Owner, record); err != nil {
			t.Fatal(err)
		}
	}
	status, reason, err := st.TurnStatus(ctx, binding.SessionID, turn)
	if err != nil || status != "failed" || reason != "synthetic placement loss" {
		t.Fatalf("recovery rewrote terminal outcome: %s %s %v", status, reason, err)
	}
	if err := st.AssertExecutionAccepted(ctx, binding.GenerationID, binding.Owner, time.Now()); err == nil {
		t.Fatal("historical recovery restored live authority")
	}
	var streams, version, completions int
	if err := st.db.QueryRow(`SELECT count(*) FROM bridge_streams`).Scan(&streams); err != nil || streams != 1 {
		t.Fatalf("recovery created a new origin: %d %v", streams, err)
	}
	if err := st.db.QueryRow(`SELECT state_version FROM session_driver_states WHERE session_id=?`, binding.SessionID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("historical completion advanced native state: %d %v", version, err)
	}
	if err := st.db.QueryRow(`SELECT count(*) FROM events WHERE type='ack_turn_completed'`).Scan(&completions); err != nil || completions != 0 {
		t.Fatalf("historical completion published success: %d %v", completions, err)
	}
	if _, err := st.db.Exec(`DELETE FROM events WHERE generation_id=?`, binding.GenerationID); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingBridgeRecords(ctx, 128)
	if err != nil || len(pending) != 2 {
		t.Fatalf("public event retention erased recovery source: %+v %v", pending, err)
	}
	if !strings.Contains(string(pending[0].Payload), "9007199254740993") || strings.Contains(string(pending[1].Payload), "driver_state_update") {
		t.Fatalf("recovery changed output or retained state command: %+v", pending)
	}
	changed := records[1]
	changed.Digest = strings.Repeat("z", 64)
	if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, binding.Owner, changed); err == nil {
		t.Fatal("contradictory historical duplicate accepted")
	}
}

func TestHistoricalRecoveryIngestAndReceiptAreAtomic(t *testing.T) {
	ctx := context.Background()
	st, binding, op, turn := recoveryFixture(t)
	if err := st.RecordJournalRecoveryHead(ctx, op.ID, op.Fence, binding.Owner, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER reject_recovery_ingress BEFORE INSERT ON bridge_ingress BEGIN SELECT RAISE(ABORT,'injected ingest failure'); END`); err != nil {
		t.Fatal(err)
	}
	record := historicalRecord(binding, turn, 1, "emit_output", `{"output_sequence":1,"payload":{"line":"durable"}}`)
	if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, binding.Owner, record); err == nil {
		t.Fatal("injected failure ignored")
	}
	watermark, err := st.BridgeStreamWatermark(ctx, binding.StoreID, binding.GenerationID)
	if err != nil || watermark != 0 {
		t.Fatalf("receipt committed before producer input: %d %v", watermark, err)
	}
	if _, err := st.db.Exec(`DROP TRIGGER reject_recovery_ingress`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, op.Fence, binding.Owner, record); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalRecoveryRequiresCurrentScopeAndCompleteHead(t *testing.T) {
	for _, fault := range []string{"owner", "fence", "origin", "turn", "gap", "head", "completion-gap", "command"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			st, binding, op, turn := recoveryFixture(t)
			if err := st.RecordJournalRecoveryHead(ctx, op.ID, op.Fence, binding.Owner, 2); err != nil {
				t.Fatal(err)
			}
			record := historicalRecord(binding, turn, 1, "emit_output", `{"output_sequence":1,"payload":{"line":"durable"}}`)
			owner, fence := binding.Owner, op.Fence
			switch fault {
			case "owner":
				owner = "generation:foreign"
			case "fence":
				fence++
			case "origin":
				record.GenerationID = "new-execution"
			case "turn":
				other := turn + 1
				record.TurnID = &other
			case "gap":
				record.Sequence = 2
			case "head":
				record.Sequence = 3
			case "completion-gap":
				record.Type = "ack_turn_completed"
				record.Payload = json.RawMessage(`{"status":"completed","final_output_sequence":1}`)
			case "command":
				record.Type = "turn_control"
			}
			if _, err := st.CommitRecoveredJournalRecord(ctx, op.ID, fence, owner, record); err == nil {
				t.Fatal("invalid historical record accepted")
			}
			watermark, err := st.BridgeStreamWatermark(ctx, binding.StoreID, binding.GenerationID)
			if err != nil || watermark != 0 {
				t.Fatalf("rejected record changed receipt: %d %v", watermark, err)
			}
			head := uint64(2)
			raw, _ := json.Marshal(execution.JournalRecoveryResult{Head: &head, ReaderStopped: true})
			if err := st.RecordEnvironmentOperationOutcome(ctx, execution.OperationOutcome{ID: op.ID, Fence: op.Fence, State: execution.OperationSucceeded, Result: raw}); err == nil {
				t.Fatal("recovery released lane with missing records")
			}
		})
	}
}
