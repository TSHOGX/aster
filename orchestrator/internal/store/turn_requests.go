package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Bound turn decisions are immutable, including no-work. They commit in the
// same transaction as claim/lease/grant changes, before a response can be sent.
// The caller must check current execution authority before reading a decision.
func readBoundTurnRequestTx(ctx context.Context, tx *sql.Tx, session, generation, owner, request, kind string, turn int64, now time.Time) (grant TurnGrant, granted, found bool, err error) {
	var recordedKind, payload string
	var recordedTurn int64
	err = tx.QueryRowContext(ctx, `SELECT kind,requested_turn_id,granted,grant_payload FROM execution_turn_requests WHERE generation_id=? AND request_id=?`, generation, request).Scan(&recordedKind, &recordedTurn, &granted, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return grant, false, false, nil
	}
	if err != nil {
		return grant, false, false, err
	}
	if recordedKind != kind || recordedTurn != turn {
		return grant, false, true, fmt.Errorf("turn request identity changed")
	}
	if err := json.Unmarshal([]byte(payload), &grant); err != nil {
		return grant, false, true, err
	}
	if granted {
		var current int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE id=? AND session_id=? AND generation_id=? AND attempt=? AND lease_owner=? AND lease_expires_at>? AND status IN ('leased','running')`, grant.TurnID, session, generation, grant.Attempt, owner, formatTime(now)).Scan(&current); err != nil {
			return grant, false, true, err
		}
		if current != 1 {
			return grant, false, true, fmt.Errorf("original turn request grant is no longer current")
		}
	}
	return grant, granted, true, nil
}

func writeBoundTurnRequestTx(ctx context.Context, tx *sql.Tx, generation, request, kind string, turn int64, grant TurnGrant, granted bool, now time.Time) error {
	payload, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_turn_requests(generation_id,request_id,kind,requested_turn_id,granted,grant_payload,created_at) VALUES(?,?,?,?,?,?,?)`, generation, request, kind, turn, granted, string(payload), formatTime(now))
	return err
}

func (s BoundBridgeStore) IdleClaimNextTurn(ctx context.Context, p ClaimNextTurnParams) (TurnGrant, bool, error) {
	return s.idleNoWork(ctx, p.SessionID, p.GenerationID, p.Owner, p.RequestID, "claim", 0, p.Now)
}
func (s BoundBridgeStore) IdleResumeTurn(ctx context.Context, p ResumeTurnParams) (TurnGrant, bool, error) {
	if p.TurnID <= 0 {
		return TurnGrant{}, false, fmt.Errorf("resume requires original turn")
	}
	return s.idleNoWork(ctx, p.SessionID, p.GenerationID, p.Owner, p.RequestID, "resume", p.TurnID, p.Now)
}

// Idle replay may answer no-work while dispatch is fenced, but that decision
// must survive acceptance too. It can never replay a previously issued grant.
func (s BoundBridgeStore) idleNoWork(ctx context.Context, session, generation, owner, request, kind string, turn int64, now time.Time) (TurnGrant, bool, error) {
	if request == "" {
		return TurnGrant{}, false, fmt.Errorf("turn request identity required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnGrant{}, false, err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(owner)); err != nil {
		return TurnGrant{}, false, err
	}
	var valid int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings b JOIN idle_execution_adoptions a ON a.generation_id=b.generation_id JOIN sandbox_environments e ON e.environment_id=b.environment_id JOIN runtime_generations g ON g.generation_id=b.generation_id JOIN sessions s ON s.active_generation_id=b.generation_id WHERE b.generation_id=? AND b.session_id=? AND b.owner=? AND a.owner=b.owner AND b.state='starting' AND b.store_id=e.store_id AND b.sandbox_id=e.sandbox_id AND b.epoch=e.epoch AND b.environment_fence=e.fence AND g.status='starting' AND g.lease_owner=b.owner AND g.lease_expires_at>? AND s.status NOT IN ('failed','destroyed') AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.session_id=b.session_id AND t.status IN ('leased','running')) AND NOT EXISTS(SELECT 1 FROM environment_operations o WHERE o.environment_id=e.environment_id AND o.state IN ('pending','issued','uncertain'))`, generation, session, owner, formatTime(now)).Scan(&valid); err != nil {
		return TurnGrant{}, false, err
	}
	if valid != 1 {
		return TurnGrant{}, false, fmt.Errorf("idle request adoption fence failed")
	}
	if _, granted, found, err := readBoundTurnRequestTx(ctx, tx, session, generation, owner, request, kind, turn, now); found || err != nil {
		if err != nil {
			return TurnGrant{}, false, err
		}
		if granted {
			return TurnGrant{}, false, fmt.Errorf("idle replay cannot deliver a grant")
		}
		return TurnGrant{}, false, tx.Commit()
	}
	if err := writeBoundTurnRequestTx(ctx, tx, generation, request, kind, turn, TurnGrant{}, false, now); err != nil {
		return TurnGrant{}, false, err
	}
	return TurnGrant{}, false, tx.Commit()
}
