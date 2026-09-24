package product

import (
	"context"
	"database/sql"
	"errors"
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

// QuiesceIdle releases idle Agent process slots while a sibling PTY or Agent
// keeps the Project active. Logical Sessions and native history are retained.
func (a *Agents) QuiesceIdle(ctx context.Context, project string, delay time.Duration) error {
	if delay <= 0 {
		delay = time.Minute
	}
	var ids []string
	err := a.Store.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT i.id,i.session_id FROM agent_incarnations i JOIN sessions s ON s.id=i.session_id WHERE i.project_id=? AND i.state='running' AND s.deletion_intent=0 AND i.last_work_at<? AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.session_id=s.id AND t.state='queued') AND NOT EXISTS(SELECT 1 FROM execution_runs r WHERE r.session_id=s.id AND r.state IN ('starting','running','stopping','unknown')) AND NOT EXISTS(SELECT 1 FROM agent_commands c WHERE c.incarnation_id=i.id AND c.state IN ('pending','sending','dispatching')) LIMIT 8`, project, sqlitex.FormatTime(a.Store.now().Add(-delay)))
		if err != nil {
			return err
		}
		type candidate struct{ id, session string }
		var work []candidate
		for rows.Next() {
			var item candidate
			if err = rows.Scan(&item.id, &item.session); err != nil {
				rows.Close()
				return err
			}
			work = append(work, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range work {
			if _, err = tx.ExecContext(ctx, `UPDATE agent_incarnations SET state='stopping' WHERE id=? AND state='running'`, item.id); err != nil {
				return err
			}
			ids = append(ids, item.session)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.Stop(ctx, project, id); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileControls rediscovers stop intents after owner loss, independently of
// whether the logical Session is blocked or has another queued prompt.
func (a *Agents) ReconcileControls(ctx context.Context, project string) error {
	// A native abort should finish promptly. If it cannot be delivered or the
	// runner never settles it, request guardian termination independently of the
	// attachment's readiness. SIGTERM retains journal/descendant cleanup; this
	// transition alone does not assert that the turn or process has ended.
	_, err := a.Store.exec(ctx, `UPDATE agent_incarnations SET state='stopping' WHERE project_id=? AND state='running' AND EXISTS (
		SELECT 1 FROM execution_runs r JOIN agent_commands c ON c.turn_id=r.prompt_id
		WHERE r.session_id=agent_incarnations.session_id AND r.state='stopping'
		AND c.incarnation_id=agent_incarnations.id AND c.kind='abort' AND c.created_at<?
	)`, project, sqlitex.FormatTime(a.Store.now().Add(-30*time.Second)))
	if err != nil {
		return err
	}
	rows, err := a.Store.reader.QueryContext(ctx, `SELECT id,session_id FROM agent_incarnations WHERE project_id=? AND state='stopping' ORDER BY last_work_at,id LIMIT 8`, project)
	if err != nil {
		return err
	}
	type pending struct{ id, session string }
	var work []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.session); err != nil {
			rows.Close()
			return err
		}
		work = append(work, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range work {
		_, err = a.Store.exec(ctx, `UPDATE agent_incarnations SET last_work_at=? WHERE id=?`, a.Store.timestamp(), item.id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		failures = append(failures, a.Stop(ctx, project, item.session))
	}
	return errors.Join(failures...)
}
