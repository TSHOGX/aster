package store

import (
	"context"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

// Next-hop lookup also serves revoked tombstones: draining an old binding must
// always address its original gateway, never a newly selected destination.
func (s *Store) BindingNextHop(ctx context.Context, binding string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(endpoint,'$.next_hop') FROM egress_route_bindings WHERE binding_id=?`, binding).Scan(&id)
	return id, err
}
func (s *Store) GrantNextHop(ctx context.Context, grant string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(r.endpoint,'$.next_hop') FROM egress_grants g JOIN egress_route_bindings r ON r.binding_id=g.binding_id WHERE g.grant_id=?`, grant).Scan(&id)
	return id, err
}
func (s *Store) ActiveGatewayBindings(ctx context.Context, nextHop, after string, limit int) ([]egress.BindingAuthority, error) {
	if limit <= 0 || limit > 128 {
		limit = 16
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.binding_id,r.revision FROM egress_route_bindings r
 JOIN execution_bindings b ON b.generation_id=r.generation_id
 JOIN sandbox_environments e ON e.environment_id=b.environment_id
 JOIN runtime_generations g ON g.generation_id=b.generation_id
 JOIN sessions s ON s.active_generation_id=b.generation_id
 JOIN orchestrator_owner o ON o.singleton=1 AND b.owner=o.uuid || ':' || ?
 WHERE json_extract(r.endpoint,'$.next_hop')=? AND r.binding_id>? AND r.state='active' AND b.state='accepted'
 AND r.backend_id=b.store_id AND r.epoch=b.epoch AND e.epoch=b.epoch AND e.fence=b.environment_fence
 AND e.store_id=b.store_id AND e.sandbox_id=b.sandbox_id AND g.lease_owner=b.owner AND g.lease_expires_at>?
 AND s.status NOT IN ('failed','destroyed')
 AND NOT EXISTS(SELECT 1 FROM session_retirements retired WHERE retired.session_id=b.session_id)
 AND NOT EXISTS(SELECT 1 FROM environment_reconcile_requests pending WHERE pending.environment_id=e.environment_id)
 AND NOT EXISTS(SELECT 1 FROM engine_backend_blocks blocked WHERE blocked.store_id=b.store_id)
 AND NOT EXISTS(SELECT 1 FROM engine_event_cursors cursor WHERE cursor.store_id=b.store_id AND cursor.resync=1)
 ORDER BY r.binding_id LIMIT ?`, RuntimeManagerRoleTag, nextHop, after, formatTime(time.Now().UTC()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []egress.BindingAuthority
	for rows.Next() {
		var b egress.BindingAuthority
		if err := rows.Scan(&b.ID, &b.Revision); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}
