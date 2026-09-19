package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

func journalRecoveryTx(ctx context.Context, tx *sql.Tx, id string, fence int64, owner string) (execution.Binding, execution.Operation, error) {
	var origin execution.Binding
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return origin, execution.Operation{}, err
	}
	op, err := operationWith(ctx, tx, id)
	if err != nil {
		return origin, op, err
	}
	var request execution.JournalRecoveryRequest
	if err := json.Unmarshal(op.Request, &request); err != nil {
		return origin, op, err
	}
	origin, err = bindingWith(ctx, tx, request.GenerationID)
	if err != nil {
		return origin, op, err
	}
	environment, err := environmentWith(ctx, tx, op.EnvironmentID)
	if err != nil {
		return origin, op, err
	}
	if op.Kind != "recover_journal" || op.Fence != fence || environment.Fence != fence || (op.State != execution.OperationIssued && op.State != execution.OperationUncertain) || origin.State != "retired" || origin.EnvironmentID != environment.ID || origin.StoreID != environment.StoreID || origin.SandboxID != environment.SandboxID || origin.Epoch >= environment.Epoch || request.Process.StoreID != environment.StoreID || request.Process.SandboxID != environment.SandboxID || request.Process.Epoch != environment.Epoch || request.Process.Handle == "" {
		return origin, op, execution.ErrStaleOperation
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM bridge_streams WHERE backend_id=? AND generation_id=? AND session_id=? AND epoch=? AND (?='' OR process_handle=?)`, origin.StoreID, origin.GenerationID, origin.SessionID, origin.Epoch, origin.ProcessHandle, origin.ProcessHandle).Scan(&count); err != nil {
		return origin, op, err
	}
	if count != 1 {
		return origin, op, execution.ErrOperationConflict
	}
	return origin, op, nil
}

func (s *Store) JournalRecoveryWatermark(ctx context.Context, id string, fence int64, owner string) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	origin, _, err := journalRecoveryTx(ctx, tx, id, fence, owner)
	if err != nil {
		return 0, err
	}
	var watermark uint64
	if err := tx.QueryRowContext(ctx, `SELECT watermark FROM bridge_streams WHERE backend_id=? AND generation_id=?`, origin.StoreID, origin.GenerationID).Scan(&watermark); err != nil {
		return 0, err
	}
	return watermark, tx.Commit()
}

func (s *Store) RecordJournalRecoveryHead(ctx context.Context, id string, fence int64, owner string, head uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	origin, op, err := journalRecoveryTx(ctx, tx, id, fence, owner)
	if err != nil {
		return err
	}
	var result execution.JournalRecoveryResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return err
	}
	if result.Head != nil && *result.Head != head {
		return fmt.Errorf("retired journal head changed during recovery")
	}
	var watermark uint64
	if err := tx.QueryRowContext(ctx, `SELECT watermark FROM bridge_streams WHERE backend_id=? AND generation_id=?`, origin.StoreID, origin.GenerationID).Scan(&watermark); err != nil {
		return err
	}
	if head < watermark {
		return fmt.Errorf("retired journal head precedes durable receipts")
	}
	result.Head = &head
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE environment_operations SET result=?,updated_at=? WHERE operation_id=? AND fence=?`, string(raw), formatTime(time.Now().UTC()), id, fence); err != nil {
		return err
	}
	return tx.Commit()
}

// CommitRecoveredJournalRecord records historical output under its original
// turn/generation. It never claims work, changes terminal classification, applies
// native state, renews a lease or grants service authority. Receipt and producer
// input commit together, independently of public event retention.
func (s *Store) CommitRecoveredJournalRecord(ctx context.Context, id string, fence int64, owner string, record execution.RecoveredJournalRecord) (int64, error) {
	if record.Sequence == 0 || len(record.Digest) != 64 || record.MessageID == "" {
		return 0, fmt.Errorf("historical journal record identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	origin, op, err := journalRecoveryTx(ctx, tx, id, fence, owner)
	if err != nil {
		return 0, err
	}
	var result execution.JournalRecoveryResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return 0, err
	}
	if result.Head == nil || record.Sequence > *result.Head || record.SessionID != origin.SessionID || record.GenerationID != origin.GenerationID {
		return 0, execution.ErrOperationConflict
	}
	var watermark uint64
	if err := tx.QueryRowContext(ctx, `SELECT watermark FROM bridge_streams WHERE backend_id=? AND generation_id=?`, origin.StoreID, origin.GenerationID).Scan(&watermark); err != nil {
		return 0, err
	}
	if err := commitBridgeStreamReceiptTx(ctx, tx, origin.StoreID, origin.GenerationID, record.Sequence, record.Digest); err != nil {
		return 0, err
	}
	if record.Sequence <= watermark {
		return 0, tx.Commit()
	}
	if record.TurnID != nil {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE id=? AND session_id=? AND generation_id=?`, *record.TurnID, origin.SessionID, origin.GenerationID).Scan(&count); err != nil {
			return 0, err
		}
		if count != 1 {
			return 0, execution.ErrOperationConflict
		}
	}
	var eventID int64
	switch record.Type {
	case "turn_control_result":
		if record.TurnID == nil {
			return 0, fmt.Errorf("historical control outcome requires original turn")
		}
		var outcome execution.SteeringOutcome
		if err := json.Unmarshal(record.Payload, &outcome); err != nil {
			return 0, err
		}
		if outcome.Status != "delivered" && outcome.Status != "rejected" {
			return 0, fmt.Errorf("historical guest steering result is invalid")
		}
		eventID, err = recordSteeringOutcomeTx(ctx, tx, origin.SessionID, origin.GenerationID, *record.TurnID, outcome)
		if err != nil {
			return 0, err
		}
	case "hello", "heartbeat", "probe_network", "claim_next_turn", "resume_turn", "ack_turn_started":
		// Retain their exact receipt only. No old protocol command is executed.
	case "emit_output":
		if record.TurnID == nil {
			return 0, fmt.Errorf("recovered output requires original turn")
		}
		var output struct {
			Sequence int64           `json:"output_sequence"`
			Stream   string          `json:"stream"`
			Payload  json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(record.Payload, &output); err != nil {
			return 0, err
		}
		if output.Sequence <= 0 {
			return 0, fmt.Errorf("positive recovered output sequence required")
		}
		var afterCompletion int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND record_key='completion') AND NOT EXISTS(SELECT 1 FROM bridge_ingress WHERE generation_id=? AND turn_id=? AND output_sequence=?)`, origin.GenerationID, *record.TurnID, origin.GenerationID, *record.TurnID, output.Sequence).Scan(&afterCompletion); err != nil {
			return 0, err
		}
		if afterCompletion != 0 {
			return 0, fmt.Errorf("recovered output follows completion boundary")
		}
		payload, err := normalizedOperationJSON(output.Payload)
		if err != nil {
			return 0, err
		}
		eventID, err = appendEventTx(ctx, tx, AppendEventParams{SessionID: origin.SessionID, GenerationID: origin.GenerationID, TurnID: record.TurnID, Type: "emit_output", OutputSequence: &output.Sequence, Stream: output.Stream, Payload: json.RawMessage(payload), Now: time.Now().UTC()})
		if err != nil {
			return 0, err
		}
	case "ack_turn_completed":
		if record.TurnID == nil {
			return 0, fmt.Errorf("recovered completion requires original turn")
		}
		var completion struct {
			Status string `json:"status"`
			Final  *int64 `json:"final_output_sequence"`
		}
		if err := json.Unmarshal(record.Payload, &completion); err != nil {
			return 0, err
		}
		if completion.Final == nil || (completion.Status != "completed" && completion.Status != "failed" && completion.Status != "canceled") {
			return 0, fmt.Errorf("recovered completion requires status and final watermark")
		}
		if err := assertCompletionOutputTx(ctx, tx, CompleteTurnParams{SessionID: origin.SessionID, GenerationID: origin.GenerationID, TurnID: *record.TurnID, FinalOutputSequence: completion.Final}); err != nil {
			return 0, err
		}
		payload, err := completeTurnEventPayload(json.RawMessage(record.Payload), CompleteTurnParams{}, false)
		if err != nil {
			return 0, err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		if _, err := appendBridgeRecordTx(ctx, tx, AppendEventParams{SessionID: origin.SessionID, GenerationID: origin.GenerationID, TurnID: record.TurnID, Type: "ack_turn_completed", Now: time.Now().UTC()}, raw); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unsupported historical journal record %q", record.Type)
	}
	return eventID, tx.Commit()
}
