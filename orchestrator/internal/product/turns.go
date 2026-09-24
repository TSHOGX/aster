package product

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

const turnColumns = `id,project_id,session_id,request_id,content,initially_held,prompt_revision,state,sequence,created_at,updated_at`
const queueColumns = `id,project_id,session_id,request_id,substr(content,1,4096),initially_held,prompt_revision,state,sequence,created_at,updated_at`

func scanTurn(row scanner) (t Turn, err error) {
	err = missing(row.Scan(&t.ID, &t.ProjectID, &t.SessionID, &t.RequestID, &t.Content, &t.InitiallyHeld, &t.PromptRevision, &t.State, &t.Sequence, &t.CreatedAt, &t.UpdatedAt))
	return
}

// AdmitTurn persists intent before a worker can dispatch. A held prompt is not
// admitted until ReleaseTurn succeeds. Request identity survives state changes.
func (s *Store) AdmitTurn(ctx context.Context, scope Scope, session, request, content string, held bool) (Turn, error) {
	if !validText(request, 128) || !validText(content, 1<<20) {
		return Turn{}, ErrInvalid
	}
	var t Turn
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		t, err = s.admitTurn(ctx, tx, scope, session, request, content, held)
		return err
	})
	return t, err
}
func (s *Store) admitTurn(ctx context.Context, tx *sql.Tx, scope Scope, session, request, content string, held bool) (Turn, error) {
	if _, err := nativePayload(map[string]string{"type": "prompt", "message": content}); err != nil {
		return Turn{}, err
	}
	var t Turn
	err := func() error {
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
		old, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id=? AND request_id=?`, session, request))
		if err == nil {
			if old.Content != content || old.InitiallyHeld != held {
				return ErrConflict
			}
			t = old
			_ = insertReceipt(ctx, tx, scope.ProjectID, session, request, "enqueue", intentDigest("enqueue", request, "", map[string]any{"content": content, "held": held}), old.ID, "", "", "accepted", map[string]any{"prompt_id": old.ID}, s.timestamp())
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		state := "queued"
		if held {
			state = "held"
		}
		now := s.timestamp()
		id := uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,state,sequence,created_at,updated_at) VALUES(?,?,?,?,?,?,?,(SELECT coalesce(max(sequence),0)+1 FROM turns WHERE session_id=?),?,?)`, id, scope.ProjectID, session, request, content, held, state, session, now, now)
		if err != nil {
			return err
		}
		if err = insertReceipt(ctx, tx, scope.ProjectID, session, request, "enqueue", intentDigest("enqueue", request, "", map[string]any{"content": content, "held": held}), id, "", "", "accepted", map[string]any{"prompt_id": id}, now); err != nil {
			return err
		}
		if !held {
			if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
				return err
			}
		}
		if err = s.bumpSession(ctx, tx, scope.ProjectID, session, "turn."+state); err != nil {
			return err
		}
		t, err = scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id=?`, id))
		return err
	}()
	return t, err
}

func (s *Store) bumpSession(ctx context.Context, tx *sql.Tx, project, session, kind string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revision=revision+1,updated_at=? WHERE id=? AND project_id=?`, s.timestamp(), session, project); err != nil {
		return err
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM sessions WHERE id=? AND project_id=?`, session, project).Scan(&rev); err != nil {
		return err
	}
	return s.event(ctx, tx, project, session, "", kind, rev)
}
func (s *Store) Turns(ctx context.Context, scope Scope, session string, after int64, limit int) ([]Turn, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE project_id=? AND session_id=? AND sequence>? ORDER BY sequence LIMIT ?`, scope.ProjectID, session, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	total := 0
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		if total+len(t.Content) > 2<<20 && len(out) > 0 {
			break
		}
		total += len(t.Content)
		out = append(out, t)
	}
	return out, rows.Err()
}

// PromptQueue is the bounded FIFO scheduling view. It deliberately excludes
// active and consumed execution records; those belong to the run/display head.
func (s *Store) PromptQueue(ctx context.Context, scope Scope, session string, after int64, limit int) (PromptQueuePage, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PromptQueuePage{}, err
	}
	defer tx.Rollback()
	child, err := sessionIn(ctx, tx, scope, session)
	if err != nil {
		return PromptQueuePage{}, err
	}
	limit = pageSize(limit)
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE project_id=? AND session_id=? AND state IN ('held','queued')`, scope.ProjectID, session).Scan(&total); err != nil {
		return PromptQueuePage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+queueColumns+`,
  coalesce((SELECT id FROM input_deliveries d WHERE d.prompt_id=turns.id ORDER BY acceptance_sequence DESC LIMIT 1),''),
  coalesce((SELECT state FROM input_deliveries d WHERE d.prompt_id=turns.id ORDER BY acceptance_sequence DESC LIMIT 1),'')
 FROM turns WHERE project_id=? AND session_id=? AND state IN ('held','queued') AND sequence>? ORDER BY sequence LIMIT ?`, scope.ProjectID, session, after, limit+1)
	if err != nil {
		return PromptQueuePage{}, err
	}
	defer rows.Close()
	items := make([]Turn, 0, limit)
	var next int64
	hasMore := false
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.SessionID, &t.RequestID, &t.Content, &t.InitiallyHeld, &t.PromptRevision, &t.State, &t.Sequence, &t.CreatedAt, &t.UpdatedAt, &t.DeliveryID, &t.DeliveryState); err != nil {
			return PromptQueuePage{}, err
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		next = t.Sequence
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return PromptQueuePage{}, err
	}
	page := PromptQueuePage{Items: items, TotalPending: total, QueueRevision: child.ControlRevision, After: after}
	if hasMore {
		page.NextAfter = next
	}
	if err := rows.Close(); err != nil {
		return PromptQueuePage{}, err
	}
	return page, tx.Commit()
}

// ReleaseTurn requires the Session control revision, because it changes that Session's
// ordered queue. A stale browser cannot release work using a sibling revision.
func (s *Store) ReleaseTurn(ctx context.Context, scope Scope, session, id string, revision int64) error {
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
		if child.ControlRevision != revision {
			return ErrConflict
		}
		t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE project_id=? AND session_id=? AND id=?`, scope.ProjectID, session, id))
		if err != nil {
			return err
		}
		if t.State != "held" {
			return ErrConflict
		}
		var steering int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_commands WHERE turn_id=? AND kind='steer' AND state IN ('pending','sending','dispatching','unknown')`, id).Scan(&steering); err != nil {
			return err
		}
		if steering > 0 {
			return ErrConflict
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE turns SET state='queued',updated_at=? WHERE id=? AND state='held'`, s.timestamp(), id)); err != nil {
			return err
		}
		if err := s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "turn.queued")
	})
}
