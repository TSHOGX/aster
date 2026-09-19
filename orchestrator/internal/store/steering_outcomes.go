package store

import (
	"context"
	"database/sql"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"strings"
	"time"
)

// RequestBoundSteer parks and associates the queued input before any guest I/O.
// Ambiguous delivery cannot let that same input be claimed as a later prompt.
func (s *Store) RequestBoundSteer(ctx context.Context, session, generation, owner string, queued, target int64) (execution.TurnCommand, error) {
	p := execution.TurnCommand{ID: fmt.Sprintf("control_steer_%s_%d", generation, queued), SessionID: session, GenerationID: generation, Owner: owner, TurnID: target, Kind: "steer"}
	if queued <= 0 || target <= 0 || queued == target {
		return p, ErrTurnNotSteerable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT content,status FROM turns WHERE id=? AND session_id=?`, queued, session).Scan(&p.Message, &status); err != nil {
		return p, err
	}
	if len(p.Message) > 1<<20 || strings.TrimSpace(p.Message) == "" {
		return p, ErrTurnNotSteerable
	}
	var prior, outcome string
	if err := tx.QueryRowContext(ctx, `SELECT command_id,outcome FROM execution_steering WHERE queued_turn_id=?`, queued).Scan(&prior, &outcome); err == nil {
		if prior != p.ID {
			return p, execution.ErrOperationConflict
		}
		if outcome == "rejected" || outcome == "unknown" {
			return p, ErrTurnNotSteerable
		}
	} else if err != sql.ErrNoRows {
		return p, err
	} else if status != "queued" {
		return p, ErrTurnNotSteerable
	}
	p, err = reserveTurnCommandTx(ctx, tx, p)
	if err != nil {
		return p, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET auto_dispatch=0 WHERE id=? AND status='queued'`, queued); err != nil {
		return p, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO execution_steering(command_id,queued_turn_id) VALUES(?,?)`, p.ID, queued); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (s *Store) RecordSteeringOutcome(ctx context.Context, session, generation, owner string, turn int64, p execution.SteeringOutcome) (int64, error) {
	if p.Status != "delivered" && p.Status != "rejected" {
		return 0, fmt.Errorf("guest steering outcome must describe delivery or rejection")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := acceptedExecutionTx(ctx, tx, session, generation, owner, time.Now().UTC()); err != nil {
		return 0, err
	}
	id, err := recordSteeringOutcomeTx(ctx, tx, session, generation, turn, p)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// Caller supplies either current accepted authority or a historical-recovery
// operation. Late outcomes stay attached to their original command and prompt.
func recordSteeringOutcomeTx(ctx context.Context, tx *sql.Tx, session, generation string, turn int64, p execution.SteeringOutcome) (int64, error) {
	if p.Status != "delivered" && p.Status != "rejected" && p.Status != "unknown" {
		return 0, fmt.Errorf("invalid steering delivery outcome")
	}
	var queued int64
	var content, previous, status string
	var event sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT q.queued_turn_id,c.message,q.outcome,q.event_id,t.status FROM execution_steering q JOIN execution_turn_commands c ON c.command_id=q.command_id JOIN turns t ON t.id=q.queued_turn_id WHERE c.command_id=? AND c.session_id=? AND c.generation_id=? AND c.turn_id=? AND c.kind='steer'`, p.CommandID, session, generation, turn).Scan(&queued, &content, &previous, &event, &status)
	if err != nil {
		return 0, err
	}
	if previous != "pending" {
		if previous != p.Status {
			return 0, execution.ErrOperationConflict
		}
		return event.Int64, nil
	}
	if status != "queued" {
		return 0, ErrTurnNotSteerable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_steering SET outcome=? WHERE command_id=?`, p.Status, p.CommandID); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	var messageID any
	typeName := "turn.steering_rejected"
	var payload any = map[string]any{"turn_id": queued, "into_turn_id": turn, "status": "rejected"}
	if p.Status == "delivered" {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='completed',auto_dispatch=0,steered_into_turn_id=?,completed_at=?,completed_by_generation=?,lease_owner=NULL,lease_expires_at=NULL WHERE id=?`, turn, formatTime(now), generation, queued); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO messages(session_id,role,content,created_at,turn_id) VALUES(?,'user',?,?,?)`, session, content, formatTime(now), queued)
		if err != nil {
			return 0, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		messageID = id
		typeName = "message.created"
		payload = Message{ID: id, SessionID: session, Role: "user", Content: content, TurnID: &queued, CreatedAt: now}
	} else if p.Status == "unknown" {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='failed',completed_at=?,completed_by_generation=?,error_class='steering_outcome_unknown',error='interrupted execution has no durable steering outcome',auto_dispatch=0 WHERE id=?`, formatTime(now), generation, queued); err != nil {
			return 0, err
		}
		typeName = "turn.steering_uncertain"
		payload = map[string]any{"turn_id": queued, "into_turn_id": turn, "status": "unknown"}
	}
	eventID, err := appendEventTx(ctx, tx, AppendEventParams{SessionID: session, GenerationID: generation, TurnID: &queued, Type: typeName, DedupeKey: "steering-result:" + p.CommandID, Payload: payload, Now: now})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_steering SET message_id=?,event_id=? WHERE command_id=?`, messageID, eventID, p.CommandID); err != nil {
		return 0, err
	}
	return eventID, nil
}
