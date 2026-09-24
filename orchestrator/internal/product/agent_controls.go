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
	class := controlWrite
	if kind == "abort" {
		class = urgentWrite
	}
	return s.writeClass(ctx, class, scope.ProjectID, 1<<20, func(tx *sql.Tx) error {
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
		i, err := liveIncarnation(ctx, tx, session)
		if err != nil {
			return err
		}
		if i.State != "running" {
			return ErrPending
		}
		if child.ControlRevision != revision {
			return ErrConflict
		}
		var active string
		if err = tx.QueryRowContext(ctx, `SELECT prompt_id FROM execution_runs WHERE session_id=? AND state IN ('starting','running','stopping','unknown') ORDER BY started_at LIMIT 1`, session).Scan(&active); err != nil {
			return missing(err)
		}
		runID := active
		if err = tx.QueryRowContext(ctx, `SELECT id FROM agent_commands WHERE turn_id=? AND kind='prompt' ORDER BY created_at LIMIT 1`, active).Scan(&runID); err != nil {
			return missing(err)
		}
		turn := active
		payload := map[string]string{"type": kind, "run_id": runID}
		if kind == "steer" {
			t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id=? AND session_id=? AND project_id=?`, source, session, scope.ProjectID))
			if err != nil {
				return err
			}
			if t.State != "held" && t.State != "queued" {
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
			if t.State == "queued" {
				if err := changed(tx.ExecContext(ctx, `UPDATE turns SET state='held',prompt_revision=prompt_revision+1,updated_at=? WHERE id=? AND state='queued'`, s.timestamp(), source)); err != nil {
					return err
				}
			}
		} else {
			if _, err = tx.ExecContext(ctx, `UPDATE turns SET state='held',updated_at=? WHERE session_id=? AND state='queued'`, s.timestamp(), session); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE execution_runs SET state='stopping' WHERE session_id=? AND prompt_id=? AND state IN ('starting','running','stopping','unknown')`, session, active); err != nil {
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
		if kind == "steer" {
			deliveryID, token, err := createDelivery(ctx, tx, scope.ProjectID, session, source, request, id, runID, "steer", s.timestamp())
			if err != nil {
				return err
			}
			payload["input_token"] = token
			raw, err = nativePayload(payload)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE agent_commands SET payload=? WHERE id=?`, raw, id); err != nil {
				return err
			}
			if err = insertReceipt(ctx, tx, scope.ProjectID, session, request, "steer", intentDigest("steer", request, source, map[string]string{"run_id": runID}), source, deliveryID, runID, "accepted", map[string]string{"delivery_id": deliveryID}, s.timestamp()); err != nil {
				return err
			}
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "command."+kind)
	})
}
