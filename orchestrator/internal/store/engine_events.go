package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
)

func (s *Store) EngineCursor(ctx context.Context, identity string) (execution.EngineCursor, error) {
	var p execution.EngineCursor
	p.StoreID = identity
	var resync int
	err := s.db.QueryRowContext(ctx, `SELECT after_sequence,resync_watermark,resync FROM engine_event_cursors WHERE store_id=?`, identity).Scan(&p.After, &p.ResyncWatermark, &resync)
	if errors.Is(err, sql.ErrNoRows) {
		var known int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sandbox_backends WHERE store_id=?`, identity).Scan(&known); err != nil {
			return p, err
		}
		if known == 0 {
			return p, execution.ErrBackendIdentityChanged
		}
		return p, nil
	}
	p.Resync = resync != 0
	return p, err
}
func ensureEngineCursorTx(ctx context.Context, tx *sql.Tx, identity string) error {
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sandbox_backends WHERE store_id=?`, identity).Scan(&known); err != nil {
		return err
	}
	if known == 0 {
		return execution.ErrBackendIdentityChanged
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO engine_event_cursors(store_id,after_sequence,resync_watermark,resync) VALUES(?,0,0,0)`, identity)
	return err
}
func (s *Store) BeginEngineResync(ctx context.Context, identity string, expected, after, watermark int64) error {
	if after < 0 || watermark <= after {
		return fmt.Errorf("invalid engine resync window")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureEngineCursorTx(ctx, tx, identity); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE engine_event_cursors SET after_sequence=?,resync_watermark=?,resync=1 WHERE store_id=? AND after_sequence=?`, after, watermark, identity, expected)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return execution.ErrStaleOperation
	}
	return tx.Commit()
}
func (s *Store) CommitEngineEvents(ctx context.Context, identity string, expected int64, events []sandbox.Event) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := ensureEngineCursorTx(ctx, tx, identity); err != nil {
		return 0, err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT after_sequence FROM engine_event_cursors WHERE store_id=?`, identity).Scan(&current); err != nil {
		return 0, err
	}
	if current != expected {
		return 0, execution.ErrStaleOperation
	}
	for _, event := range events {
		if event.ID == "" || event.SandboxID == "" || event.Sequence <= 0 {
			return 0, fmt.Errorf("engine event identity and sequence required")
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return 0, err
		}
		hash := sha256.Sum256(raw)
		digest := hex.EncodeToString(hash[:])
		var previous string
		var sequence int64
		err = tx.QueryRowContext(ctx, `SELECT sequence,digest FROM engine_event_inbox WHERE store_id=? AND event_id=?`, identity, event.ID).Scan(&sequence, &previous)
		if err == nil {
			if sequence != event.Sequence || digest != previous {
				return 0, fmt.Errorf("contradictory engine event replay")
			}
			// The original inbox transaction already applied this invalidation.
			// Reapplying it could revoke readiness obtained after that event was
			// reconciled. Resync can rewind the cursor, so still advance through
			// previously committed events without replaying their side effects.
			current = max(current, event.Sequence)
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		} else {
			if event.Sequence <= current {
				return 0, fmt.Errorf("unrecorded historical event below durable cursor")
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO engine_event_inbox(store_id,event_id,sequence,sandbox_id,kind,epoch,payload,digest) VALUES(?,?,?,?,?,?,?,?)`, identity, event.ID, event.Sequence, event.SandboxID, event.Kind, event.Epoch, string(raw), digest)
			if err != nil {
				return 0, err
			}
		}
		// Events only invalidate current acceptance; they never overwrite accepted
		// epoch/revision or reactivate authority after a newer inspection.
		_, err = tx.ExecContext(ctx, `INSERT INTO environment_reconcile_requests(environment_id,reason,event_sequence) SELECT environment_id,?,? FROM sandbox_environments WHERE store_id=? AND sandbox_id=? ON CONFLICT(environment_id) DO UPDATE SET reason=excluded.reason,event_sequence=max(environment_reconcile_requests.event_sequence,excluded.event_sequence)`, event.Kind, event.Sequence, identity, event.SandboxID)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sandbox_environments SET observed_state='unknown' WHERE store_id=? AND sandbox_id=?`, identity, event.SandboxID); err != nil {
			return 0, err
		}
		if event.Sequence > current {
			current = event.Sequence
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE engine_event_cursors SET after_sequence=? WHERE store_id=?`, current, identity)
	if err != nil {
		return 0, err
	}
	return current, tx.Commit()
}

func (s *Store) CommitEnvironmentInspection(ctx context.Context, p execution.EnvironmentInspection) error {
	if p.EventSequence < -1 {
		return fmt.Errorf("invalid inspection event watermark")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	environment, err := environmentWith(ctx, tx, p.EnvironmentID)
	if err != nil {
		return err
	}
	if environment.Fence != p.Fence {
		return execution.ErrStaleOperation
	}
	if p.Observed.StoreID != environment.StoreID {
		return execution.ErrBackendIdentityChanged
	}
	if p.Observed.SandboxID != environment.SandboxID || environment.SandboxID == "" {
		return execution.ErrOperationConflict
	}
	var later int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_reconcile_requests WHERE environment_id=? AND event_sequence>?`, environment.ID, p.EventSequence).Scan(&later); err != nil {
		return err
	}
	if later != 0 {
		return execution.ErrStaleOperation
	}
	validState := (p.Observed.State == "running" && p.Observed.PlacementState == "active") || (p.Observed.State == "paused" && p.Observed.PlacementState == "lost")
	if !validState || p.Observed.Epoch != environment.Epoch || p.Observed.MutationRevision != environment.MutationRevision {
		if _, err := tx.ExecContext(ctx, `UPDATE sandbox_environments SET observed_state='unknown' WHERE environment_id=?`, environment.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO environment_reconcile_requests(environment_id,reason,event_sequence) VALUES(?,'placement_changed',?) ON CONFLICT(environment_id) DO UPDATE SET reason=excluded.reason,event_sequence=max(environment_reconcile_requests.event_sequence,excluded.event_sequence)`, environment.ID, max(p.EventSequence, 0)); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return execution.ErrResumeConflict
	}
	// Running inspection does not re-enable protocol readiness. Only a current
	// agent handshake can clear an execution's invalidation and grant authority.
	if p.Observed.State == "paused" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM environment_reconcile_requests WHERE environment_id=? AND event_sequence<=?`, environment.ID, p.EventSequence); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sandbox_environments SET observed_state=? WHERE environment_id=?`, p.Observed.State, environment.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FinishEngineResync(ctx context.Context, identity string, expected int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE engine_event_cursors SET resync=0 WHERE store_id=? AND after_sequence=? AND after_sequence>=resync_watermark`, identity, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return execution.ErrStaleOperation
	}
	return nil
}

// A missing request has sequence -1 so a newly queued inventory request at
// sequence zero cannot be mistaken for one covered by earlier evidence.
func (s *Store) EnvironmentInvalidationSequence(ctx context.Context, environment string) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT event_sequence FROM environment_reconcile_requests WHERE environment_id=?`, environment).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return sequence, err
}
