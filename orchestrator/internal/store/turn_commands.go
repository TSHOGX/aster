package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"strings"
	"time"
)

func (s *Store) ReserveTurnCommand(ctx context.Context, p execution.TurnCommand) (execution.TurnCommand, error) {
	if !strings.HasPrefix(p.ID, "control_") || p.SessionID == "" || p.GenerationID == "" || p.Owner == "" || p.TurnID <= 0 || (p.Kind != "abort" && p.Kind != "steer") || len(p.Message) > 1<<20 || (p.Kind == "steer" && strings.TrimSpace(p.Message) == "") || (p.Kind == "abort" && p.Message != "") {
		return p, fmt.Errorf("valid scoped turn command required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	command, err := reserveTurnCommandTx(ctx, tx, p)
	if err != nil {
		return p, err
	}
	return command, tx.Commit()
}
func reserveTurnCommandTx(ctx context.Context, tx *sql.Tx, p execution.TurnCommand) (execution.TurnCommand, error) {
	if err := acceptedExecutionTx(ctx, tx, p.SessionID, p.GenerationID, p.Owner, time.Now().UTC()); err != nil {
		return p, err
	}
	var previous execution.TurnCommand
	err := tx.QueryRowContext(ctx, `SELECT command_id,session_id,generation_id,turn_id,owner,kind,message,acknowledged FROM execution_turn_commands WHERE command_id=?`, p.ID).Scan(&previous.ID, &previous.SessionID, &previous.GenerationID, &previous.TurnID, &previous.Owner, &previous.Kind, &previous.Message, &previous.Acknowledged)
	if err == nil {
		p.Acknowledged = previous.Acknowledged
		// The persisted owner records who issued the immutable intent. Current
		// delivery authority comes from the accepted same-process binding.
		previous.Owner = p.Owner
		if p != previous {
			return p, execution.ErrOperationConflict
		}
		return previous, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE id=? AND session_id=? AND generation_id=? AND lease_owner=? AND status IN ('leased','running') AND lease_expires_at>?`, p.TurnID, p.SessionID, p.GenerationID, p.Owner, formatTime(time.Now().UTC())).Scan(&active); err != nil {
		return p, err
	}
	if active != 1 {
		return p, fmt.Errorf("turn command target is not executing")
	}
	if p.Kind == "steer" {
		binding, err := bindingWith(ctx, tx, p.GenerationID)
		if err != nil {
			return p, err
		}
		if binding.Driver != "pi" {
			return p, fmt.Errorf("driver does not support steering")
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_turn_commands WHERE generation_id=? AND turn_id=?`, p.GenerationID, p.TurnID).Scan(&count); err != nil {
		return p, err
	}
	if count >= 64 {
		return p, fmt.Errorf("turn command limit reached")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_turn_commands(command_id,session_id,generation_id,turn_id,owner,kind,message,created_at) VALUES(?,?,?,?,?,?,?,?)`, p.ID, p.SessionID, p.GenerationID, p.TurnID, p.Owner, p.Kind, p.Message, formatTime(time.Now().UTC())); err != nil {
		return p, err
	}
	if p.Kind == "abort" {
		if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=? AND turn_id=?`, p.GenerationID, p.TurnID); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (s *Store) TurnCommandGrantIDs(ctx context.Context, p execution.TurnCommand) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.grant_id FROM egress_grants g JOIN execution_turn_commands c ON c.generation_id=g.generation_id AND c.turn_id=g.turn_id JOIN execution_bindings b ON b.generation_id=c.generation_id WHERE c.command_id=? AND b.owner=? AND c.kind='abort' ORDER BY g.grant_id`, p.ID, p.Owner)
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

func (s *Store) RequestBoundInterrupt(ctx context.Context, session, generation, owner string) (*execution.TurnCommand, SessionRuntimeSnapshot, error) {
	var snapshot SessionRuntimeSnapshot
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, snapshot, err
	}
	defer tx.Rollback()
	if err := acceptedExecutionTx(ctx, tx, session, generation, owner, time.Now().UTC()); err != nil {
		return nil, snapshot, err
	}
	if _, err := parkQueuedTurnsTx(ctx, tx, session); err != nil {
		return nil, snapshot, err
	}
	var turn int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM turns WHERE session_id=? AND generation_id=? AND status IN ('leased','running') ORDER BY sequence LIMIT 1`, session, generation).Scan(&turn)
	var command *execution.TurnCommand
	if err == nil {
		p, err := reserveTurnCommandTx(ctx, tx, execution.TurnCommand{ID: fmt.Sprintf("control_abort_%s_%d", generation, turn), SessionID: session, GenerationID: generation, Owner: owner, TurnID: turn, Kind: "abort"})
		if err != nil {
			return nil, snapshot, err
		}
		command = &p
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, snapshot, err
	}
	if command == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET status='running_idle',updated_at=? WHERE id=?`, formatTime(time.Now().UTC()), session); err != nil {
			return nil, snapshot, err
		}
	}
	snapshot, err = getSessionRuntimeSnapshot(ctx, tx, session)
	if err != nil {
		return nil, snapshot, err
	}
	return command, snapshot, tx.Commit()
}

func (s *Store) AcknowledgeTurnCommand(ctx context.Context, b execution.Binding, id string) error {
	// Ordinary protocol responses also receive wire ACKs. Only reserved control
	// identities participate in this application command ledger.
	if !strings.HasPrefix(id, "control_") {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertOwnerTx(ctx, tx, ownerUUIDFromLeaseOwner(b.Owner)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_turn_commands SET acknowledged=1 WHERE command_id=? AND session_id=? AND generation_id=? AND EXISTS(SELECT 1 FROM execution_bindings b WHERE b.generation_id=execution_turn_commands.generation_id AND b.owner=?) AND EXISTS(SELECT 1 FROM bridge_streams s WHERE s.backend_id=? AND s.generation_id=execution_turn_commands.generation_id AND s.process_handle=? AND s.epoch=?)`, id, b.SessionID, b.GenerationID, b.Owner, b.StoreID, b.ProcessHandle, b.Epoch)
	if err != nil {
		return err
	}
	if err := requireOneRow(result, "turn command acknowledgment scope mismatch"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) TurnCommandAcknowledged(ctx context.Context, p execution.TurnCommand) (bool, error) {
	var ack bool
	err := s.db.QueryRowContext(ctx, `SELECT acknowledged FROM execution_turn_commands WHERE command_id=? AND session_id=? AND generation_id=? AND turn_id=? AND EXISTS(SELECT 1 FROM execution_bindings b WHERE b.generation_id=execution_turn_commands.generation_id AND b.owner=?) AND kind=? AND message=?`, p.ID, p.SessionID, p.GenerationID, p.TurnID, p.Owner, p.Kind, p.Message).Scan(&ack)
	return ack, err
}
