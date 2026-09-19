package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/execution"
)

func deferredRequestIdentity(b execution.Binding, raw json.RawMessage) (string, error) {
	var envelope struct {
		SessionID    string `json:"session_id"`
		GenerationID string `json:"generation_id"`
		RequestID    string `json:"request_id"`
		MessageID    string `json:"message_id"`
		Type         string `json:"type"`
		TurnID       *int64 `json:"turn_id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	if envelope.SessionID != b.SessionID || envelope.GenerationID != b.GenerationID || (envelope.Type != "claim_next_turn" && envelope.Type != "resume_turn") {
		return "", fmt.Errorf("deferred turn request scope or type mismatch")
	}
	if envelope.Type == "resume_turn" && (envelope.TurnID == nil || *envelope.TurnID <= 0) {
		return "", fmt.Errorf("deferred resume requires original turn")
	}
	if envelope.RequestID == "" {
		envelope.RequestID = envelope.MessageID
	}
	if envelope.RequestID == "" {
		return "", fmt.Errorf("deferred request identity required")
	}
	return envelope.RequestID, nil
}

// Receipts may settle during adoption or event invalidation, but never across
// owner, process, placement, lease or lifecycle fences. Sending is checked
// separately against acceptedExecutionTx.
func deferredRequestScopeTx(ctx context.Context, tx *sql.Tx, b execution.Binding, adopting bool) error {
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(b.Owner)); err != nil {
		return err
	}
	var valid int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_bindings b JOIN sandbox_environments e ON e.environment_id=b.environment_id JOIN runtime_generations g ON g.generation_id=b.generation_id JOIN sessions s ON s.active_generation_id=b.generation_id JOIN bridge_streams stream ON stream.backend_id=b.store_id AND stream.generation_id=b.generation_id AND stream.process_handle=b.process_handle AND stream.epoch=b.epoch WHERE b.generation_id=? AND b.session_id=? AND b.owner=? AND b.store_id=? AND b.sandbox_id=? AND b.process_handle=? AND b.epoch=? AND b.environment_fence=? AND b.store_id=e.store_id AND b.sandbox_id=e.sandbox_id AND b.epoch=e.epoch AND b.environment_fence=e.fence AND g.lease_owner=b.owner AND g.lease_expires_at>? AND g.status IN ('starting','idle','active') AND s.status NOT IN ('failed','destroyed') AND ((?=0 AND b.state='accepted') OR (b.state='starting' AND EXISTS(SELECT 1 FROM idle_execution_adoptions a WHERE a.generation_id=b.generation_id AND a.owner=b.owner))) AND NOT EXISTS(SELECT 1 FROM environment_operations o WHERE o.environment_id=e.environment_id AND o.state IN ('pending','issued','uncertain'))`, b.GenerationID, b.SessionID, b.Owner, b.StoreID, b.SandboxID, b.ProcessHandle, b.Epoch, b.EnvironmentFence, formatTime(time.Now().UTC()), adopting).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return fmt.Errorf("deferred request execution scope mismatch")
	}
	return nil
}

func (s *Store) DeferTurnRequest(ctx context.Context, b execution.Binding, raw json.RawMessage) error {
	request, err := deferredRequestIdentity(b, raw)
	if err != nil {
		return err
	}
	canonical, err := normalizedOperationJSON(raw)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deferredRequestScopeTx(ctx, tx, b, true); err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT envelope FROM deferred_turn_requests WHERE generation_id=? AND request_id=?`, b.GenerationID, request).Scan(&previous)
	if err == nil {
		if previous != string(canonical) {
			return fmt.Errorf("contradictory deferred request replay")
		}
		// An identical replay can follow a lost host receipt. Re-sending the
		// immutable response is safe; obsolete grants never become pending.
		if _, err := tx.ExecContext(ctx, `UPDATE deferred_turn_requests SET state='pending' WHERE generation_id=? AND request_id=? AND state='delivered'`, b.GenerationID, request); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO deferred_turn_requests(generation_id,request_id,envelope) VALUES(?,?,?)`, b.GenerationID, request, string(canonical)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PendingDeferredTurnRequests(ctx context.Context, b execution.Binding, after int64, limit int) (execution.DeferredTurnRequestPage, error) {
	page := execution.DeferredTurnRequestPage{After: after}
	if after < 0 || limit <= 0 || limit > 128 {
		return page, fmt.Errorf("bounded deferred request cursor required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := deferredRequestScopeTx(ctx, tx, b, false); err != nil {
		return page, err
	}
	if err := acceptedExecutionTx(ctx, tx, b.SessionID, b.GenerationID, b.Owner, time.Now().UTC()); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,envelope FROM deferred_turn_requests WHERE generation_id=? AND state='pending' AND sequence>? ORDER BY sequence LIMIT ?`, b.GenerationID, after, limit)
	if err != nil {
		return page, err
	}
	var candidates []execution.DeferredTurnRequest
	for rows.Next() {
		var p execution.DeferredTurnRequest
		var raw string
		if err := rows.Scan(&p.Sequence, &raw); err != nil {
			rows.Close()
			return page, err
		}
		p.Envelope = json.RawMessage(raw)
		candidates = append(candidates, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	for _, p := range candidates {
		page.After = p.Sequence
		request, err := deferredRequestIdentity(b, p.Envelope)
		if err != nil {
			return page, err
		}
		var granted bool
		var payload string
		err = tx.QueryRowContext(ctx, `SELECT granted,grant_payload FROM execution_turn_requests WHERE generation_id=? AND request_id=?`, b.GenerationID, request).Scan(&granted, &payload)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return page, err
		}
		if err == nil && granted {
			var grant TurnGrant
			if err := json.Unmarshal([]byte(payload), &grant); err != nil {
				return page, err
			}
			var terminal int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE id=? AND session_id=? AND generation_id=? AND attempt=? AND completed_by_generation=? AND status IN ('completed','failed','canceled')`, grant.TurnID, b.SessionID, b.GenerationID, grant.Attempt, b.GenerationID).Scan(&terminal); err != nil {
				return page, err
			}
			if terminal == 1 {
				if _, err := tx.ExecContext(ctx, `UPDATE deferred_turn_requests SET state='obsolete' WHERE sequence=?`, p.Sequence); err != nil {
					return page, err
				}
				continue
			}
		}
		page.Requests = append(page.Requests, p)
	}
	return page, tx.Commit()
}

func (s *Store) PrepareDeferredTurnReply(ctx context.Context, b execution.Binding, request, command string) error {
	if request == "" || command == "" {
		return fmt.Errorf("deferred reply identities required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous, state string
	err = tx.QueryRowContext(ctx, `SELECT command_id,state FROM deferred_turn_requests WHERE generation_id=? AND request_id=?`, b.GenerationID, request).Scan(&previous, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit() // An ordinary live response, not deferred work.
	}
	if err != nil {
		return err
	}
	if state == "obsolete" || (previous != "" && previous != command) {
		return fmt.Errorf("deferred reply identity or outcome changed")
	}
	if err := deferredRequestScopeTx(ctx, tx, b, false); err != nil {
		return err
	}
	if err := acceptedExecutionTx(ctx, tx, b.SessionID, b.GenerationID, b.Owner, time.Now().UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deferred_turn_requests SET command_id=? WHERE generation_id=? AND request_id=?`, command, b.GenerationID, request); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeDeferredTurnReply(ctx context.Context, b execution.Binding, command string) error {
	if command == "" {
		return fmt.Errorf("deferred reply command identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM deferred_turn_requests WHERE generation_id=? AND command_id=?`, b.GenerationID, command).Scan(&known); err != nil {
		return err
	}
	if known == 0 {
		return tx.Commit()
	}
	if err := deferredRequestScopeTx(ctx, tx, b, false); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deferred_turn_requests SET state='delivered' WHERE generation_id=? AND command_id=? AND state='pending'`, b.GenerationID, command); err != nil {
		return err
	}
	return tx.Commit()
}
