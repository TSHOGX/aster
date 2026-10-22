package projections

import (
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/workflow"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type RunLease struct {
	RunID     string    `json:"run_id"`
	WorkerID  string    `json:"worker_id"`
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ClaimRun claims queued work or replaces an expired worker. Every successful
// claim advances the persisted fence, including a reclaim by the same worker ID.
func (s *Store) ClaimRun(ctx context.Context, run, worker string, duration time.Duration) (RunLease, error) {
	result := RunLease{RunID: run, WorkerID: worker}
	if !manifest.ValidID(run) || !manifest.ValidID(worker) || duration < time.Second || duration > 5*time.Minute {
		return result, fmt.Errorf("invalid run lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result.ExpiresAt = now.Add(duration)
	err = tx.QueryRowContext(ctx, `UPDATE runs SET state=CASE WHEN state IN ('cancelling','pause_requested') THEN state ELSE 'running' END,worker_id=?,fence=fence+1,lease_until=? WHERE id=? AND (state='queued' OR (state IN ('running','pause_requested','cancelling') AND lease_until<=?)) RETURNING fence`, worker, result.ExpiresAt.UnixNano(), run, now.UnixNano()).Scan(&result.Fence)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrConflict
		}
		return result, err
	}
	return result, tx.Commit()
}
func (s *Store) RenewRun(ctx context.Context, lease RunLease, duration time.Duration) (RunLease, error) {
	if duration < time.Second || duration > 5*time.Minute || lease.Fence < 1 {
		return lease, fmt.Errorf("invalid lease renewal")
	}
	now := time.Now().UTC()
	expires := now.Add(duration)
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET lease_until=? WHERE id=? AND worker_id=? AND fence=? AND state IN ('running','pause_requested','cancelling') AND lease_until>?`, expires.UnixNano(), lease.RunID, lease.WorkerID, lease.Fence, now.UnixNano())
	if err != nil {
		return lease, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return lease, err
	}
	if n != 1 {
		return lease, ErrConflict
	}
	lease.ExpiresAt = expires
	return lease, nil
}

// SettleRun only settles a live lease. Replays of its identical terminal state
// succeed; a different worker/fence or terminal result never changes that result.
func (s *Store) SettleRun(ctx context.Context, lease RunLease, state string) error {
	if state != "completed" && state != "failed" && state != "cancelled" {
		return fmt.Errorf("invalid terminal run state")
	}
	if lease.Fence < 1 {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, worker string
	var fence, expires int64
	if err = tx.QueryRowContext(ctx, "SELECT state,worker_id,fence,lease_until FROM runs WHERE id=?", lease.RunID).Scan(&current, &worker, &fence, &expires); err != nil {
		return err
	}
	if worker != lease.WorkerID || fence != lease.Fence {
		return ErrConflict
	}
	if current == state {
		return nil
	}
	if current == "pause_requested" && expires > time.Now().UnixNano() {
		return workflow.ErrPauseRequested
	}
	if (current != "running" && !(current == "cancelling" && state == "cancelled")) || expires <= time.Now().UnixNano() {
		return ErrConflict
	}
	var held int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM run_budget_reservations WHERE run_id=? AND state='held'", lease.RunID).Scan(&held); err != nil {
		return err
	}
	if held > 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE runs SET state=?,lease_until=0 WHERE id=?", state, lease.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

// AcknowledgeRunPause is called only after the engine has drained admitted nodes.
// It refuses uncertain provider work and fences the old worker at the boundary.
func (s *Store) AcknowledgeRunPause(ctx context.Context, lease RunLease) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err = liveRunBudget(ctx, tx, lease, false); err != nil {
		return err
	}
	var held int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM run_budget_reservations WHERE run_id=? AND state='held'", lease.RunID).Scan(&held); err != nil {
		return err
	}
	if held != 0 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, "UPDATE runs SET state='paused',fence=fence+1,worker_id='',lease_until=0 WHERE id=? AND state='pause_requested'", lease.RunID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}
