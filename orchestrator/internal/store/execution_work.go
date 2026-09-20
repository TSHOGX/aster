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

	case execution.ReconcileOperation:
		query = `SELECT o.operation_id,e.session_id,'',o.kind FROM environment_operations o JOIN sandbox_environments e ON e.environment_id=o.environment_id
 WHERE o.operation_id>? AND o.fence=e.fence AND (
  (o.state IN ('pending','issued','uncertain') AND o.kind IN ('create_volume','create','configure','pause','resume')) OR
  (o.state='succeeded' AND o.kind='create_volume' AND NOT EXISTS(SELECT 1 FROM session_storage_bindings v WHERE v.environment_id=e.environment_id AND v.purpose=json_extract(o.request,'$.Purpose'))) OR
  (o.state='succeeded' AND EXISTS(SELECT 1 FROM execution_bindings b WHERE o.operation_id='idle-pause:' || b.generation_id AND b.state!='retired')))
 AND NOT EXISTS(SELECT 1 FROM workspace_write_maintenance w WHERE w.operation_id=o.operation_id AND w.stage!='complete')
 AND NOT EXISTS(SELECT 1 FROM native_capture_activations c WHERE c.operation_id=o.operation_id AND c.stage!='complete')
 AND NOT EXISTS(SELECT 1 FROM execution_recoveries r WHERE r.operation_id=o.operation_id AND r.stage!='complete')
 AND o.operation_id!='retire-pause:' || e.session_id
 ORDER BY o.operation_id LIMIT ?`
	case execution.ReconcileStart:
		query = `SELECT b.generation_id,b.session_id,b.generation_id,'' FROM execution_bindings b JOIN runtime_generations g ON g.generation_id=b.generation_id
 WHERE b.generation_id>? AND b.state='starting' AND g.status='starting'
 AND NOT EXISTS(SELECT 1 FROM session_retirements r WHERE r.session_id=b.session_id)
 AND NOT EXISTS(SELECT 1 FROM execution_start_failures failed WHERE failed.generation_id=b.generation_id)
 AND NOT EXISTS(SELECT 1 FROM idle_execution_adoptions a WHERE a.generation_id=b.generation_id)
 AND NOT EXISTS(SELECT 1 FROM active_execution_adoptions a WHERE a.generation_id=b.generation_id)
 AND NOT EXISTS(SELECT 1 FROM execution_recoveries r WHERE r.generation_id=b.generation_id AND r.stage!='complete')
 ORDER BY b.generation_id LIMIT ?`
	case execution.ReconcilePreparation:
		query = `SELECT e.environment_id,e.session_id,'','' FROM sandbox_environments e JOIN sessions s ON s.id=e.session_id
 WHERE e.environment_id>? AND s.status NOT IN ('failed','destroyed')
 AND (e.sandbox_id IS NULL OR e.sandbox_id='' OR
 (coalesce(json_extract(nullif(e.applied_configuration,''),'$.generation_id'),'')!='' AND NOT EXISTS(SELECT 1 FROM execution_bindings b WHERE b.generation_id=json_extract(e.applied_configuration,'$.generation_id'))) OR
 (e.observed_state='running' AND coalesce(json_extract(nullif(e.applied_configuration,''),'$.generation_id'),'')=''))
 AND NOT EXISTS(SELECT 1 FROM execution_bindings b WHERE b.environment_id=e.environment_id AND b.state!='retired')
 AND NOT EXISTS(SELECT 1 FROM environment_operations o WHERE o.environment_id=e.environment_id AND o.state IN ('pending','issued','uncertain','failed'))
 AND NOT EXISTS(SELECT 1 FROM session_retirements r WHERE r.session_id=e.session_id)
 ORDER BY e.environment_id LIMIT ?`
	case execution.ReconcileRetirement:
		query = `SELECT session_id,session_id,'','' FROM session_retirements WHERE session_id>? AND state='requested' ORDER BY session_id LIMIT ?`
	case execution.ReconcileEnvironment:
		query = `SELECT e.environment_id,e.session_id,'','' FROM environment_reconcile_requests r JOIN sandbox_environments e ON e.environment_id=r.environment_id WHERE e.environment_id>? AND e.sandbox_id!='' AND NOT EXISTS(SELECT 1 FROM execution_bindings b WHERE b.environment_id=e.environment_id AND b.state!='retired') ORDER BY e.environment_id LIMIT ?`
	case execution.ReconcileActive:
		query = `SELECT b.generation_id,b.session_id,b.generation_id,'' FROM execution_bindings b JOIN runtime_generations g ON g.generation_id=b.generation_id JOIN sessions s ON s.active_generation_id=b.generation_id WHERE b.generation_id>? AND s.status NOT IN ('failed','destroyed') AND NOT EXISTS(SELECT 1 FROM session_retirements r WHERE r.session_id=s.id) AND NOT EXISTS(SELECT 1 FROM execution_recoveries r WHERE r.generation_id=b.generation_id AND r.stage!='complete') AND ((b.state='accepted' AND g.status='active' AND EXISTS(SELECT 1 FROM turns t WHERE t.generation_id=b.generation_id AND t.status IN ('leased','running')) AND (b.detached=1 OR b.owner!=? OR g.lease_expires_at<=? OR EXISTS(SELECT 1 FROM environment_reconcile_requests r WHERE r.environment_id=b.environment_id))) OR (b.state='starting' AND EXISTS(SELECT 1 FROM active_execution_adoptions a WHERE a.generation_id=b.generation_id AND a.state='adopting'))) ORDER BY b.generation_id LIMIT ?`
		args = append(args, owner, formatTime(time.Now().UTC()))
	case execution.ReconcileIdle:
		query = `SELECT b.generation_id,b.session_id,b.generation_id,'' FROM execution_bindings b JOIN runtime_generations g ON g.generation_id=b.generation_id JOIN sessions s ON s.active_generation_id=b.generation_id WHERE b.generation_id>? AND s.status NOT IN ('failed','destroyed') AND NOT EXISTS(SELECT 1 FROM session_retirements r WHERE r.session_id=s.id) AND NOT EXISTS(SELECT 1 FROM execution_recoveries r WHERE r.generation_id=b.generation_id AND r.stage!='complete') AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.session_id=b.session_id AND t.status IN ('leased','running')) AND ((b.state='accepted' AND g.status IN ('idle','active') AND (b.detached=1 OR b.owner!=? OR g.lease_expires_at<=? OR EXISTS(SELECT 1 FROM environment_reconcile_requests r WHERE r.environment_id=b.environment_id))) OR (b.state='starting' AND g.status='starting' AND EXISTS(SELECT 1 FROM idle_execution_adoptions a WHERE a.generation_id=b.generation_id))) ORDER BY b.generation_id LIMIT ?`
		args = append(args, owner, formatTime(time.Now().UTC()))
	case execution.ReconcileAbort:
		query = `SELECT c.command_id,c.session_id,c.generation_id,'' FROM execution_turn_commands c JOIN turns t ON t.id=c.turn_id JOIN execution_bindings b ON b.generation_id=c.generation_id JOIN runtime_generations g ON g.generation_id=b.generation_id WHERE c.command_id>? AND c.kind='abort' AND c.acknowledged=0 AND b.owner=? AND b.state='accepted' AND t.generation_id=c.generation_id AND t.status IN ('leased','running') AND t.lease_owner=b.owner AND t.lease_expires_at>? AND g.lease_owner=b.owner AND g.lease_expires_at>? ORDER BY c.command_id LIMIT ?`
		args = append(args, owner, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
	case execution.ReconcileSteer:
		query = `SELECT c.command_id,c.session_id,c.generation_id,'' FROM execution_turn_commands c JOIN execution_steering q ON q.command_id=c.command_id AND q.outcome='pending' JOIN turns t ON t.id=c.turn_id JOIN execution_bindings b ON b.generation_id=c.generation_id JOIN runtime_generations g ON g.generation_id=b.generation_id WHERE c.command_id>? AND c.kind='steer' AND c.acknowledged=0 AND b.owner=? AND b.state='accepted' AND t.generation_id=c.generation_id AND t.status IN ('leased','running') AND t.lease_owner=b.owner AND t.lease_expires_at>? AND g.lease_owner=b.owner AND g.lease_expires_at>? ORDER BY c.command_id LIMIT ?`
		args = append(args, owner, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
	case execution.ReconcileWorkspace:
		query = `SELECT m.operation_id,m.session_id,'','' FROM workspace_mutations m JOIN workspace_guest_mutations g ON g.operation_id=m.operation_id WHERE m.operation_id>? AND m.state IN ('active','uncertain') ORDER BY m.operation_id LIMIT ?`
	case execution.ReconcileCapture:
		query = `SELECT session_id,session_id,'','' FROM native_capture_activations WHERE session_id>? AND stage!='complete' ORDER BY session_id LIMIT ?`
	case execution.ReconcileInterrupted:
		query = `SELECT generation_id,session_id,generation_id,reason FROM (
 SELECT r.generation_id,b.session_id,r.reason FROM execution_recoveries r JOIN execution_bindings b ON b.generation_id=r.generation_id WHERE r.stage!='complete'
 UNION ALL
 SELECT f.generation_id,b.session_id,f.reason FROM execution_start_failures f JOIN execution_bindings b ON b.generation_id=f.generation_id WHERE b.state='starting' AND NOT EXISTS(SELECT 1 FROM execution_recoveries r WHERE r.generation_id=f.generation_id)
 ) WHERE generation_id>? ORDER BY generation_id LIMIT ?`
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
			p.Owner = owner
		}
	}
	return work, tx.Commit()
}
