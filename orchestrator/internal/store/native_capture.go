package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
