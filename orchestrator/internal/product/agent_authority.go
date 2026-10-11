package product

import (
	"context"
	"database/sql"
	"errors"
)

// rejectAgentQueue withdraws only work whose non-dispatch is a durable host
// fact. Prompts remain held with a visible rejection; restoring a grant alone
// cannot restart them. Sending/native work retains its settlement authority.
func (s *Store) rejectAgentQueue(ctx context.Context, tx *sql.Tx, project, session string) error {
	reason := "permission_revoked"
	var status string
	var held bool
	if err := tx.QueryRowContext(ctx, `SELECT a.status,p.execution_suspended FROM projects p JOIN accounts a ON a.id=p.owner_id WHERE p.id=?`, project).Scan(&status, &held); err != nil {
		return err
	}
	if status != "active" || held {
		reason = "account_suspended"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,kind FROM agent_commands WHERE session_id=? AND state='pending' AND kind IN ('prompt','steer')`, session)
	if err != nil {
		return err
	}
	type command struct{ id, kind string }
	var commands []command
	for rows.Next() {
		var c command
		if err = rows.Scan(&c.id, &c.kind); err != nil {
			break
		}
		commands = append(commands, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	now := s.timestamp()
	for _, c := range commands {
		if err = restoreUndelivered(ctx, tx, c.id, "rejected", reason, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE turns SET dispatch_error=? WHERE id IN (SELECT prompt_id FROM input_deliveries WHERE command_id=?)`, reason, c.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='rejected',error=? WHERE id=?`, reason, c.id); err != nil {
			return err
		}
		if c.kind == "prompt" {
			if _, err = tx.ExecContext(ctx, `UPDATE execution_runs SET state='failed',outcome='failed',settled_at=?,reconciled_at=? WHERE id=?`, now, now, c.id); err != nil {
				return err
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE turns SET queue_policy='held',dispatch_error=?,updated_at=? WHERE session_id=? AND disposition='pending' AND queue_policy='released'`, reason, now, session)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 && len(commands) == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state='idle' WHERE id=? AND state IN ('starting','running') AND NOT EXISTS(SELECT 1 FROM execution_runs WHERE session_id=? AND state IN ('starting','running','stopping','unknown') AND acknowledged=0)`, session, session); err != nil {
		return err
	}
	return s.bumpSession(ctx, tx, project, session, "turn."+reason)
}

// agentAdmission fences new provisioning while allowing reattachment to an
// existing incarnation for journal recovery, stopping, and settlement.
func (s *Store) agentAdmission(ctx context.Context, scope Scope, session string) error {
	// The dispatch/reservation transactions below are authoritative. This fast
	// path avoids an extra writer turn for ordinary authorized worker polling.
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return err
	}
	if err := authorizeExecution(ctx, s.reader, scope, ActionAgentsRun); err == nil {
		return nil
	}
	var denial error
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := sessionIn(ctx, tx, scope, session); err != nil {
			return err
		}
		denial = authorizeExecution(ctx, tx, scope, ActionAgentsRun)
		if denial == nil {
			return nil
		}
		if !errors.Is(denial, ErrForbidden) && !errors.Is(denial, ErrUnauthorized) {
			return denial
		}
		if err := s.rejectAgentQueue(ctx, tx, scope.ProjectID, session); err != nil {
			return err
		}
		_, err := liveIncarnation(ctx, tx, session)
		if err == nil {
			denial = nil
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return denial
}
