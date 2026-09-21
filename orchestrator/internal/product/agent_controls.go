package product

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// Interrupt requests a native abort and holds queued prompts in the same
// transaction. The running turn isn't canceled until native completion evidence.
func (s *Store) Interrupt(ctx context.Context, scope Scope, session, request string, revision int64) error {
	return s.control(ctx, scope, session, request, "", "abort", revision)
}

// Steer delivers an explicitly held source prompt into the active turn. Its
// source isn't completed until a successful native steer response is committed.
func (s *Store) Steer(ctx context.Context, scope Scope, session, source, request string, revision int64) error {
	return s.control(ctx, scope, session, request, source, "steer", revision)
}
func (s *Store) control(ctx context.Context, scope Scope, session, request, source, kind string, revision int64) error {
	if !validText(request, 128) {
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
		if child.Deletion != Live {
			return ErrDeleting
		}
		i, err := liveIncarnation(ctx, tx, session)
		if err != nil {
			return err
		}
		if i.State != "running" {
			return ErrPending
		}
		// Deterministic command ID is scoped to Session and request, with enough
		// entropy that callers cannot collide with generated prompt command IDs.
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(session+"\x00control\x00"+request)).String()
		var oldKind, oldTurn string
		err = tx.QueryRowContext(ctx, `SELECT kind,coalesce(turn_id,'') FROM agent_commands WHERE id=?`, id).Scan(&oldKind, &oldTurn)
		if err == nil {
			if oldKind != kind || kind == "steer" && oldTurn != source {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if child.ControlRevision != revision {
			return ErrConflict
		}
		var active string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM turns WHERE session_id=? AND state IN ('starting','running','stopping')`, session).Scan(&active); err != nil {
			return missing(err)
		}
		turn := active
		payload := map[string]string{"type": kind}
		if kind == "steer" {
			t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id=? AND session_id=? AND project_id=?`, source, session, scope.ProjectID))
			if err != nil {
				return err
			}
			if t.State != "held" {
				return ErrConflict
			}
			payload["message"] = t.Content
			turn = source
			var pending int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_commands WHERE turn_id=? AND kind='steer' AND state IN ('pending','sending','dispatching','unknown')`, source).Scan(&pending); err != nil {
				return err
			}
			if pending > 0 {
				return ErrConflict
			}
		} else {
			if _, err = tx.ExecContext(ctx, `UPDATE turns SET state='held',updated_at=? WHERE session_id=? AND state='queued'`, s.timestamp(), session); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE turns SET state='stopping',updated_at=? WHERE id=?`, s.timestamp(), active); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state='stopping' WHERE id=?`, session); err != nil {
				return err
			}
		}
		raw, err := nativePayload(payload)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_commands(id,project_id,session_id,incarnation_id,turn_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?,'pending',?)`, id, scope.ProjectID, session, i.ID, turn, kind, raw, s.timestamp()); err != nil {
			return err
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "command."+kind)
	})
}
