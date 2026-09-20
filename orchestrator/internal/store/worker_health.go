package store

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/health"
)

func (s *Store) RecordWorkerHealth(ctx context.Context, owner string, samples []health.Status) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if owner != "" {
		if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
			return err
		}
	}
	for _, sample := range samples {
		if sample.Name == "" || sample.StaleAfter <= 0 || (owner == "" && sample.Name != "native_capture") {
			return fmt.Errorf("worker health identity and bounds required")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO worker_health(name,owner,state,observed_at,last_success_at,error,stale_after_ns) VALUES(?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET owner=excluded.owner,state=excluded.state,observed_at=excluded.observed_at,last_success_at=excluded.last_success_at,error=excluded.error,stale_after_ns=excluded.stale_after_ns`, sample.Name, owner, sample.State, formatTime(sample.ObservedAt), formatTime(sample.LastSuccessAt), sample.Error, int64(sample.StaleAfter))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
