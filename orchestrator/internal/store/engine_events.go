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
