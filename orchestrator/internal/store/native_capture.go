package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/nativecapture"
	"harness-platform/orchestrator/internal/sandbox"
	"time"
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

func (s *Store) PendingNativeCaptures(ctx context.Context, after string, limit int) ([]nativecapture.Request, error) {
	if limit <= 0 || limit > 128 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.session_id,s.user_id,s.status,r.state_version,r.state_digest,r.generation_id,r.turn_id,r.store_id,r.sandbox_id,r.epoch,r.native_id,r.snapshot_path,r.native_digest,r.size,r.created_at
 FROM native_capture_requests r JOIN sessions s ON s.id=r.session_id
 WHERE r.session_id>? AND (r.retry_after='' OR r.retry_after<=?) AND NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=r.session_id AND c.state_version=r.state_version)
 AND NOT EXISTS(SELECT 1 FROM native_capture_requests older WHERE older.session_id=r.session_id AND (older.retry_after='' OR older.retry_after<=?) AND (older.attempts<r.attempts OR (older.attempts=r.attempts AND older.state_version<r.state_version)) AND NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=older.session_id AND c.state_version=older.state_version))
 ORDER BY r.session_id,r.state_version LIMIT ?`, after, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []nativecapture.Request
	for rows.Next() {
		var p nativecapture.Request
		var created string
		if err := rows.Scan(&p.SessionID, &p.UserID, &p.Status, &p.StateVersion, &p.StateDigest, &p.GenerationID, &p.TurnID, &p.StoreID, &p.SandboxID, &p.Epoch, &p.NativeID, &p.SnapshotPath, &p.NativeDigest, &p.Size, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(created)
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) CompleteNativeCapture(ctx context.Context, p nativecapture.Request, result nativecapture.Result) error {
	if result.ArchivePath == "" {
		return fmt.Errorf("durable native archive required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM native_capture_requests WHERE session_id=? AND state_version=? AND state_digest=? AND generation_id=? AND turn_id=? AND store_id=? AND sandbox_id=? AND epoch=? AND native_id=? AND snapshot_path=? AND native_digest=? AND size=?`, p.SessionID, p.StateVersion, p.StateDigest, p.GenerationID, p.TurnID, p.StoreID, p.SandboxID, p.Epoch, p.NativeID, p.SnapshotPath, p.NativeDigest, p.Size).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("native capture request identity changed")
	}
	var previous nativecapture.Result
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT archive_path,native_digest FROM native_capture_receipts WHERE session_id=? AND state_version=?`, p.SessionID, p.StateVersion).Scan(&previous.ArchivePath, &digest)
	if err == nil {
		if previous != result || digest != p.NativeDigest {
			return fmt.Errorf("native capture receipt changed")
		}
		return tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO native_capture_receipts(session_id,state_version,native_digest,archive_path,completed_at) VALUES(?,?,?,?,?)`, p.SessionID, p.StateVersion, p.NativeDigest, result.ArchivePath, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit()
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

// Full immutable snapshots are independent. Failure backoff leaves other versions
// eligible and is durable across capture-worker replacement.
func (s *Store) RecordNativeCaptureFailure(ctx context.Context, p nativecapture.Request, message string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempts FROM native_capture_requests WHERE session_id=? AND state_version=? AND state_digest=? AND native_digest=?`, p.SessionID, p.StateVersion, p.StateDigest, p.NativeDigest).Scan(&attempts); err != nil {
		return err
	}
	delay := 30 * time.Second * time.Duration(1<<min(attempts, 4))
	_, err = tx.ExecContext(ctx, `UPDATE native_capture_requests SET attempts=min(attempts+1,1000000),retry_after=?,last_error=? WHERE session_id=? AND state_version=? AND NOT EXISTS(SELECT 1 FROM native_capture_receipts c WHERE c.session_id=native_capture_requests.session_id AND c.state_version=native_capture_requests.state_version)`, formatTime(now.Add(delay)), message, p.SessionID, p.StateVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}
