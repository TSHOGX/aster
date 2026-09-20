package store

import (
	"context"
	"fmt"
	"time"
)

// PrepareTurnEgressDrain closes admission durably before remote flow teardown.
func (s *Store) PrepareTurnEgressDrain(ctx context.Context, p CompleteTurnParams) ([]string, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := executionIngressTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.Now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=? AND turn_id=?`, p.GenerationID, p.TurnID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_id FROM egress_grants WHERE generation_id=? AND turn_id=? ORDER BY grant_id`, p.GenerationID, p.TurnID)
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
	return ids, tx.Commit()
}
func (s *Store) PendingGatewayGrantDrains(ctx context.Context, hop, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 128 {
		limit = 16
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.grant_id FROM egress_grant_drains d JOIN egress_grants g ON g.grant_id=d.grant_id JOIN egress_route_bindings r ON r.binding_id=g.binding_id WHERE d.state='pending' AND json_extract(r.endpoint,'$.next_hop')=? AND d.grant_id>? ORDER BY d.grant_id LIMIT ?`, hop, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) ConfirmGatewayGrantDrained(ctx context.Context, grant string) error {
	return s.confirmGatewayDrain(ctx, "g.grant_id=?", grant)
}
func (s *Store) ConfirmGatewayBindingDrained(ctx context.Context, binding string) error {
	return s.confirmGatewayDrain(ctx, "g.binding_id=?", binding)
}
func (s *Store) confirmGatewayDrain(ctx context.Context, predicate, id string) error {
	if id == "" {
		return fmt.Errorf("drained authority identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM egress_grants g WHERE `+predicate+` AND g.state!='revoked'`, id).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("gateway drain requires closed admission")
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grant_drains SET state='drained',confirmed_at=? WHERE state='pending' AND grant_id IN (SELECT g.grant_id FROM egress_grants g WHERE `+predicate+`)`, now, id); err != nil {
		return err
	}
	// A missing finish report cannot establish a business outcome or byte count.
	// Preserve that uncertainty while recording verified transport closure.
	if _, err := tx.ExecContext(ctx, `UPDATE egress_requests SET outcome='transport_closed',finished_at=? WHERE outcome IS NULL AND grant_id IN (SELECT g.grant_id FROM egress_grants g WHERE `+predicate+`)`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}
