package product

import (
	"context"
	"database/sql"
)

// Placement loss ends process incarnations, not logical Sessions. Unknown turns
// remain unknown; idle Sessions may start fresh runners from retained native state.
func (s *Store) loseStaleAgents(ctx context.Context, tx *sql.Tx, project string, epoch uint64, reason string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,session_id FROM agent_incarnations WHERE project_id=? AND epoch<>? AND state<>'exited'`, project, epoch)
	if err != nil {
		return err
	}
	type lost struct{ id, session string }
	var items []lost
	for rows.Next() {
		var item lost
		if err = rows.Scan(&item.id, &item.session); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_incarnations SET state='exited',owner=owner+1,error=? WHERE id=?`, reason, item.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='unknown',error=? WHERE incarnation_id=? AND state IN ('sending','dispatching','applied')`, reason, item.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE turns SET state='unknown',updated_at=? WHERE session_id=? AND state IN ('starting','running','stopping')`, s.timestamp(), item.session); err != nil {
			return err
		}
		var unknown int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND state='unknown'`, item.session).Scan(&unknown); err != nil {
			return err
		}
		state, failure := "idle", ""
		if unknown > 0 {
			state, failure = "blocked", reason
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state=?,failure=? WHERE id=? AND deletion_intent<2`, state, failure, item.session); err != nil {
			return err
		}
		// Once the entire placement is gone, descendants cannot still be executing.
		if _, err = tx.ExecContext(ctx, `UPDATE project_activity SET state='complete',updated_at=? WHERE project_id=? AND session_id=? AND kind IN ('agent_start','agent_control','descendants')`, s.timestamp(), project, item.session); err != nil {
			return err
		}
		if err = s.bumpSession(ctx, tx, project, item.session, "session.placement_lost"); err != nil {
			return err
		}
	}
	return nil
}
func (a *Agents) Reconcile(ctx context.Context, project string) error {
	rows, err := a.Store.reader.QueryContext(ctx, `SELECT id,session_id FROM agent_incarnations WHERE project_id=? AND state<>'exited'`, project)
	if err != nil {
		return err
	}
	live := map[string]string{}
	for rows.Next() {
		var id, session string
		if err = rows.Scan(&id, &session); err != nil {
			rows.Close()
			return err
		}
		live[session] = id
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	a.mu.Lock()
	var stale []*agentAttachment
	for session, attachment := range a.attachments {
		if attachment.incarnation.ProjectID == project && live[session] != attachment.incarnation.ID {
			stale = append(stale, attachment)
			delete(a.attachments, session)
		}
	}
	a.mu.Unlock()
	for _, attachment := range stale {
		attachment.cancel()
	}
	return nil
}
