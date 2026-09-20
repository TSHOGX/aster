package store

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"time"
)

// RetainedIdleCandidates pages accepted bindings eligible for idle release.

func (s *Store) RetainedIdleCandidates(ctx context.Context, owner, after string, threshold time.Time, limit int) ([]execution.Binding, error) {
	if limit <= 0 || limit > 128 {
		return nil, fmt.Errorf("bounded idle candidate page required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.generation_id FROM execution_bindings b JOIN sessions s ON s.active_generation_id=b.generation_id JOIN runtime_generations g ON g.generation_id=b.generation_id WHERE b.generation_id>? AND b.state='accepted' AND (EXISTS(SELECT 1 FROM environment_operations o WHERE o.operation_id='idle-pause:' || b.generation_id AND o.environment_id=b.environment_id AND o.state IN ('pending','issued','uncertain','succeeded')) OR (b.owner=? AND g.status='idle' AND s.status='running_idle' AND coalesce(s.last_activity_at,s.updated_at)<=? AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.session_id=s.id AND t.status IN ('queued','leased','running')))) ORDER BY b.generation_id LIMIT ?`, after, owner, formatTime(threshold), limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var result []execution.Binding
	for _, id := range ids {
		b, err := bindingWith(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, tx.Commit()
}
