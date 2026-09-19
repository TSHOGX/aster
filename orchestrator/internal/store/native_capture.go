package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

// The capture request and native state advance share the completion transaction.
// A slow external archive consumer cannot block subsequent product work.
func recordNativeCaptureRequestTx(ctx context.Context, tx *sql.Tx, p CompleteTurnParams, payload []byte) error {
	binding, err := bindingWith(ctx, tx, p.GenerationID)
	if err != nil {
		return err
	}
	var state struct {
		ID   string `json:"selected_session_id"`
		Path string `json:"selected_session_relpath"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_capture_requests(session_id,state_version,state_digest,generation_id,turn_id,store_id,sandbox_id,epoch,native_id,selected_path,snapshot_path,native_digest,size,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.SessionID, p.DriverStateUpdate.StateVersion, p.DriverStateUpdate.StateDigest, p.GenerationID, p.TurnID, binding.StoreID, binding.SandboxID, binding.Epoch, state.ID, state.Path, p.DriverStateEvidence.NativeSnapshotPath, p.DriverStateEvidence.NativeDigest, p.DriverStateEvidence.NativeSize, formatTime(p.Now))
	return err
}

func nativeRestoreRequestTx(ctx context.Context, tx *sql.Tx, binding execution.Binding, environment execution.Environment) (execution.NativeRestoreRequest, error) {
	state, err := getDriverStateTx(ctx, tx, binding.SessionID, "pi")
	if err != nil {
		return execution.NativeRestoreRequest{}, err
	}
	request := execution.NativeRestoreRequest{Reference: sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID, Epoch: environment.Epoch, MutationRevision: environment.MutationRevision}, StateDigest: state.StateDigest, StateVersion: state.StateVersion}
	var payload struct {
		Kind string `json:"state_kind"`
		ID   string `json:"selected_session_id"`
		Path string `json:"selected_session_relpath"`
	}
	if err := json.Unmarshal(state.StatePayload, &payload); err != nil {
		return request, err
	}
	if payload.Kind == "pi_uninitialized" {
		return request, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT snapshot_path,selected_path,native_id,native_digest,size FROM native_capture_requests WHERE session_id=? AND state_version=? AND state_digest=? AND store_id=? AND sandbox_id=?`, binding.SessionID, state.StateVersion, state.StateDigest, environment.StoreID, environment.SandboxID).Scan(&request.SnapshotPath, &request.SelectedPath, &request.NativeID, &request.NativeDigest, &request.Size)
	if err != nil {
		return request, fmt.Errorf("completed native snapshot unavailable: %w", err)
	}
	if request.NativeID != payload.ID || request.SelectedPath != payload.Path {
		return request, execution.ErrOperationConflict
	}
	return request, nil
}
