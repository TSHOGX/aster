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
