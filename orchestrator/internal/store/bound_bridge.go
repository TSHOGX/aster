package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BoundBridgeStore serves executions whose authority is an accepted opaque
// binding. It never allocates addresses, reads runtime proofs or registers a
// source-IP model context. Gateways use separately scoped service grants.

func (s *Store) BridgeProtocolEvidence(ctx context.Context, session, generation string) (BridgeProtocolEvidence, error) {
	binding, err := s.GetExecutionBinding(ctx, generation)
	if err != nil {
		return BridgeProtocolEvidence{}, err
	}
	if binding.SessionID != session || binding.State == "retired" {
		return BridgeProtocolEvidence{}, fmt.Errorf("bridge execution binding unavailable")
	}
	return BridgeProtocolEvidence{DriverID: binding.Driver, ProtocolVersion: binding.ProtocolVersion, TurnInputSchema: binding.TurnInputSchema, AgentManifestDigest: binding.ManifestDigest}, nil
}

func (s *Store) ClaimNextTurn(ctx context.Context, p ClaimNextTurnParams) (TurnGrant, bool, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.LeaseTTL <= 0 || p.RequestID == "" {
		return TurnGrant{}, false, fmt.Errorf("turn lease and request identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnGrant{}, false, err
	}
	defer tx.Rollback()
	if err := acceptedExecutionTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.Now); err != nil {
		return TurnGrant{}, false, err
	}
	if grant, granted, found, err := readBoundTurnRequestTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.RequestID, "claim", 0, p.Now); found || err != nil {
		if err != nil {
			return TurnGrant{}, false, err
		}
		return grant, granted, tx.Commit()
	}
	finish := func(grant TurnGrant, granted bool) (TurnGrant, bool, error) {
		if err := writeBoundTurnRequestTx(ctx, tx, p.GenerationID, p.RequestID, "claim", 0, grant, granted, p.Now); err != nil {
			return TurnGrant{}, false, err
		}
		return grant, granted, tx.Commit()
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND status IN ('leased','running')`, p.SessionID).Scan(&busy); err != nil {
		return TurnGrant{}, false, err
	}
	if busy != 0 {
		return finish(TurnGrant{}, false)
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM turns WHERE session_id=? AND status='queued' AND auto_dispatch=1 ORDER BY sequence LIMIT 1`, p.SessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return finish(TurnGrant{}, false)
	}
	if err != nil {
		return TurnGrant{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE turns SET status='leased',generation_id=?,lease_owner=?,lease_expires_at=?,claim_request_id=?,claim_granted_at=?,attempt=attempt+1 WHERE id=? AND status='queued' AND auto_dispatch=1`, p.GenerationID, p.Owner, formatTime(p.Now.Add(p.LeaseTTL)), p.RequestID, formatTime(p.Now), id)
	if err != nil {
		return TurnGrant{}, false, err
	}
	if err := requireOneRow(result, "bound turn claim CAS failed"); err != nil {
		return TurnGrant{}, false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runtime_generations SET status='active' WHERE generation_id=?`, p.GenerationID)
	if err != nil {
		return TurnGrant{}, false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET status='running_active',updated_at=? WHERE id=? AND active_generation_id=?`, formatTime(p.Now), p.SessionID, p.GenerationID)
	if err != nil {
		return TurnGrant{}, false, err
	}
	if err := issueBoundGrantsTx(ctx, tx, p.GenerationID, id, p.Now.Add(p.LeaseTTL)); err != nil {
		return TurnGrant{}, false, err
	}
	grant, err := boundTurnGrantTx(ctx, tx, p.SessionID, p.GenerationID, id, p.Owner, p.Now)
	if err != nil {
		return TurnGrant{}, false, err
	}
	return finish(grant, true)
}

func (s *Store) ResumeTurn(ctx context.Context, p ResumeTurnParams) (TurnGrant, bool, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	if p.LeaseTTL <= 0 || p.RequestID == "" || p.TurnID <= 0 {
		return TurnGrant{}, false, fmt.Errorf("positive turn lease, turn and request identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TurnGrant{}, false, err
	}
	defer tx.Rollback()
	if err := acceptedExecutionTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.Now); err != nil {
		return TurnGrant{}, false, err
	}
	if grant, granted, found, err := readBoundTurnRequestTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.RequestID, "resume", p.TurnID, p.Now); found || err != nil {
		if err != nil {
			return TurnGrant{}, false, err
		}
		return grant, granted, tx.Commit()
	}
	finish := func(grant TurnGrant, granted bool) (TurnGrant, bool, error) {
		if err := writeBoundTurnRequestTx(ctx, tx, p.GenerationID, p.RequestID, "resume", p.TurnID, grant, granted, p.Now); err != nil {
			return TurnGrant{}, false, err
		}
		return grant, granted, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE turns SET lease_expires_at=? WHERE id=? AND session_id=? AND generation_id=? AND lease_owner=? AND lease_expires_at>? AND status IN ('leased','running')`, formatTime(p.Now.Add(p.LeaseTTL)), p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now))
	if err != nil {
		return TurnGrant{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return TurnGrant{}, false, err
	}
	if affected == 0 {
		return finish(TurnGrant{}, false)
	}
	if err := issueBoundGrantsTx(ctx, tx, p.GenerationID, p.TurnID, p.Now.Add(p.LeaseTTL)); err != nil {
		return TurnGrant{}, false, err
	}
	grant, err := boundTurnGrantTx(ctx, tx, p.SessionID, p.GenerationID, p.TurnID, p.Owner, p.Now)
	if err != nil {
		return TurnGrant{}, false, err
	}
	grant.Replayed = true
	return finish(grant, true)
}

func (s *Store) AckTurnStarted(ctx context.Context, p AckStartedParams) (int64, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := executionIngressTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, p.Now); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE turns SET status='running',started_at=coalesce(started_at,?),ack_started_at=coalesce(ack_started_at,?) WHERE id=? AND session_id=? AND generation_id=? AND lease_owner=? AND lease_expires_at>? AND status IN ('leased','running')`, formatTime(p.Now), formatTime(p.Now), p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(p.Now))
	if err != nil {
		return 0, err
	}
	if err := requireOneRow(result, "bound turn start CAS failed"); err != nil {
		return 0, err
	}
	if _, _, err := materializeTurnMessageTx(ctx, tx, p.SessionID, p.TurnID, p.Now); err != nil {
		return 0, err
	}
	var eventID int64
	if p.EventType != "" {
		eventID, err = appendEventTx(ctx, tx, AppendEventParams{SessionID: p.SessionID, GenerationID: p.GenerationID, TurnID: &p.TurnID, DedupeKey: p.EventDedupeKey, Type: p.EventType, Payload: p.EventPayload, Now: p.Now})
		if err != nil {
			return 0, err
		}
	}
	return eventID, tx.Commit()
}

func boundTurnGrantTx(ctx context.Context, tx *sql.Tx, session, generation string, turn int64, owner string, now time.Time) (TurnGrant, error) {
	var grant TurnGrant
	var payload, expires string
	err := tx.QueryRowContext(ctx, `SELECT t.id,t.sequence,t.content,t.attempt,t.lease_expires_at,ds.driver_id,ds.state_digest,ds.state_version,ds.state_payload FROM turns t JOIN execution_bindings b ON b.generation_id=t.generation_id JOIN session_driver_states ds ON ds.session_id=t.session_id AND ds.driver_id=b.driver_id WHERE t.id=? AND t.session_id=? AND t.generation_id=? AND t.lease_owner=? AND t.lease_expires_at>? AND t.status IN ('leased','running')`, turn, session, generation, owner, formatTime(now)).Scan(&grant.TurnID, &grant.Sequence, &grant.Content, &grant.Attempt, &expires, &grant.DriverState.DriverID, &grant.DriverState.StateDigest, &grant.DriverState.StateVersion, &payload)
	grant.ExpiresAt = parseTime(expires)
	grant.DriverStatePayload = []byte(payload)
	return grant, err
}
