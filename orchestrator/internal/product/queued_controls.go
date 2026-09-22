package product

import (
	"context"
	"database/sql"
)

func (s *Store) ChangeQueuedTurn(ctx context.Context, scope Scope, session, id, state string, revision int64) error {
	if state != "held" && state != "canceled" {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.ControlRevision != revision {
			return ErrConflict
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_commands WHERE turn_id=? AND kind='steer' AND state IN ('pending','sending','dispatching','unknown')`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrBusy
		}
		if err = changed(tx.ExecContext(ctx, `UPDATE turns SET state=?,updated_at=? WHERE id=? AND session_id=? AND state IN ('held','queued')`, state, s.timestamp(), id, session)); err != nil {
			return err
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "turn."+state)
	})
}
