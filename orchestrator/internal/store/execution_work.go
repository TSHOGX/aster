package store

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"time"
)

// PendingExecutionWork enumerates durable obligations without acquiring remote
// authority. Actions recheck ownership/bindings immediately before side effects.
func (s *Store) PendingExecutionWork(ctx context.Context, kind execution.ReconcileKind, owner, after string, limit int) ([]execution.ReconcileWork, error) {
	if limit <= 0 || limit > 128 {
		limit = 16
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return nil, err
	}
	query := ""
	args := []any{after}
	switch kind {
	case execution.ReconcileAbort:
		query = `SELECT c.command_id,c.session_id,c.generation_id,'' FROM execution_turn_commands c JOIN turns t ON t.id=c.turn_id JOIN execution_bindings b ON b.generation_id=c.generation_id JOIN runtime_generations g ON g.generation_id=b.generation_id WHERE c.command_id>? AND c.kind='abort' AND c.acknowledged=0 AND c.owner=? AND b.owner=c.owner AND b.state='accepted' AND t.generation_id=c.generation_id AND t.status IN ('leased','running') AND t.lease_owner=c.owner AND t.lease_expires_at>? AND g.lease_owner=c.owner AND g.lease_expires_at>? ORDER BY c.command_id LIMIT ?`
		args = append(args, owner, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
	case execution.ReconcileSteer:
		query = `SELECT c.command_id,c.session_id,c.generation_id,'' FROM execution_turn_commands c JOIN execution_steering q ON q.command_id=c.command_id AND q.outcome='pending' JOIN turns t ON t.id=c.turn_id JOIN execution_bindings b ON b.generation_id=c.generation_id JOIN runtime_generations g ON g.generation_id=b.generation_id WHERE c.command_id>? AND c.kind='steer' AND c.acknowledged=0 AND c.owner=? AND b.owner=c.owner AND b.state='accepted' AND t.generation_id=c.generation_id AND t.status IN ('leased','running') AND t.lease_owner=c.owner AND t.lease_expires_at>? AND g.lease_owner=c.owner AND g.lease_expires_at>? ORDER BY c.command_id LIMIT ?`
		args = append(args, owner, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
	case execution.ReconcileWorkspace:
		query = `SELECT m.operation_id,m.session_id,'','' FROM workspace_mutations m JOIN workspace_guest_mutations g ON g.operation_id=m.operation_id WHERE m.operation_id>? AND m.state IN ('active','uncertain') ORDER BY m.operation_id LIMIT ?`
	case execution.ReconcileCapture:
		query = `SELECT session_id,session_id,'','' FROM native_capture_activations WHERE session_id>? AND stage!='complete' ORDER BY session_id LIMIT ?`
	case execution.ReconcileInterrupted:
		query = `SELECT r.generation_id,b.session_id,r.generation_id,r.reason FROM execution_recoveries r JOIN execution_bindings b ON b.generation_id=r.generation_id WHERE r.generation_id>? AND r.stage!='complete' ORDER BY r.generation_id LIMIT ?`
	default:
		return nil, fmt.Errorf("unsupported reconciliation obligation")
	}
	rows, err := tx.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var work []execution.ReconcileWork
	for rows.Next() {
		var item execution.ReconcileWork
		if err := rows.Scan(&item.Key, &item.SessionID, &item.GenerationID, &item.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		work = append(work, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if kind == execution.ReconcileAbort || kind == execution.ReconcileSteer {
		for i := range work {
			p := &work[i].Command
			if err := tx.QueryRowContext(ctx, `SELECT command_id,session_id,generation_id,owner,turn_id,kind,message,acknowledged FROM execution_turn_commands WHERE command_id=?`, work[i].Key).Scan(&p.ID, &p.SessionID, &p.GenerationID, &p.Owner, &p.TurnID, &p.Kind, &p.Message, &p.Acknowledged); err != nil {
				return nil, err
			}
		}
	}
	return work, tx.Commit()
}
