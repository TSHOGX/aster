package product

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
)

const promptStatus = `CASE turns.disposition
 WHEN 'pending' THEN CASE turns.queue_policy WHEN 'held' THEN 'held' ELSE 'queued' END
 WHEN 'claimed' THEN coalesce((SELECT CASE r.state WHEN 'settled' THEN r.outcome ELSE r.state END
  FROM execution_runs r WHERE r.prompt_id=turns.id ORDER BY r.started_at DESC,r.id DESC LIMIT 1),'claimed')
 WHEN 'canceled' THEN 'canceled'
 WHEN 'handled' THEN 'handled'
 ELSE coalesce((SELECT CASE r.state WHEN 'settled' THEN r.outcome ELSE r.state END
  FROM input_deliveries d JOIN execution_runs r ON r.id=d.run_id
  WHERE d.prompt_id=turns.id AND d.state='consumed' ORDER BY d.acceptance_sequence DESC LIMIT 1),'consumed') END`
const turnColumns = `id,project_id,session_id,request_id,content,initially_held,prompt_revision,queue_policy,disposition,` + promptStatus + `,sequence,created_at,updated_at,dispatch_error`
const queueColumns = `id,project_id,session_id,request_id,substr(content,1,4096),initially_held,prompt_revision,queue_policy,disposition,` + promptStatus + `,sequence,created_at,updated_at,dispatch_error`

func scanTurn(row scanner) (t Turn, err error) {
	err = missing(row.Scan(&t.ID, &t.ProjectID, &t.SessionID, &t.RequestID, &t.Content, &t.InitiallyHeld, &t.PromptRevision, &t.QueuePolicy, &t.Disposition, &t.State, &t.Sequence, &t.CreatedAt, &t.UpdatedAt, &t.DispatchError))
	return
}

// AdmitTurn persists intent before a worker can dispatch. A held prompt is not
// eligible until a release mutation succeeds. Request identity survives state changes.
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
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		if err := authorizeExecution(ctx, tx, scope, ActionAgentsRun); err != nil {
			return err
		}
		old, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id=? AND request_id=?`, session, request))
		if err == nil {
			if old.Content != content || old.InitiallyHeld != held {
				return ErrConflict
			}
			t = old
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		var used bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM control_receipts WHERE session_id=? AND request_id=?)`, session, request).Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrConflict
		}
		state := "queued"
		if held {
			state = "held"
		}
		now := s.timestamp()
		id := uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,queue_policy,sequence,created_at,updated_at) VALUES(?,?,?,?,?,?,?,(SELECT coalesce(max(sequence),0)+1 FROM turns WHERE session_id=?),?,?)`, id, scope.ProjectID, session, request, content, held, map[bool]string{true: "held", false: "released"}[held], session, now, now)
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
	if _, err := readableSession(ctx, s.reader, scope, session); err != nil {
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
func (s *Store) PromptQueue(ctx context.Context, scope Scope, session string, after int64, limit int, observedFrontier *string) (PromptQueuePage, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PromptQueuePage{}, err
	}
	defer tx.Rollback()
	child, err := readableSession(ctx, tx, scope, session)
	if err != nil {
		return PromptQueuePage{}, err
	}
	var observedSource int64
	if observedFrontier != nil && *observedFrontier != "" {
		if len(*observedFrontier) > 4096 {
			return PromptQueuePage{}, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.DecodeString(*observedFrontier)
		if err != nil {
			return PromptQueuePage{}, ErrInvalid
		}
		var identity []string
		if json.Unmarshal(raw, &identity) != nil || len(identity) != 2 || identity[0] == "" {
			return PromptQueuePage{}, ErrInvalid
		}
		sequence, err := strconv.ParseInt(identity[1], 10, 64)
		if err != nil || sequence <= 0 {
			return PromptQueuePage{}, ErrInvalid
		}
		if err = tx.QueryRowContext(ctx, `SELECT source_id FROM conversation_sources WHERE session_id=? AND incarnation_id=? AND sequence=?`, session, identity[0], sequence).Scan(&observedSource); err != nil {
			return PromptQueuePage{}, missing(err)
		}
	}
	limit = pageSize(limit)
	var total int
	const queued = `(disposition IN ('pending','claimed') OR (disposition='consumed' AND (
 NOT EXISTS(SELECT 1 FROM conversation_items ci WHERE ci.session_id=turns.session_id AND ci.id=turns.id||'/user')
 OR (? AND EXISTS(SELECT 1 FROM input_deliveries d JOIN execution_runs r ON r.id=d.run_id
 JOIN conversation_sources c ON c.incarnation_id=r.incarnation_id AND c.sequence=d.consumed_sequence
 WHERE d.prompt_id=turns.id AND d.state='consumed' AND c.source_id>?)))))`
	observed := observedFrontier != nil
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE project_id=? AND session_id=? AND `+queued, scope.ProjectID, session, observed, observedSource).Scan(&total); err != nil {
		return PromptQueuePage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+queueColumns+`,
		coalesce((SELECT id FROM input_deliveries d WHERE d.prompt_id=turns.id ORDER BY acceptance_sequence DESC LIMIT 1),''),
  coalesce((SELECT state FROM input_deliveries d WHERE d.prompt_id=turns.id ORDER BY acceptance_sequence DESC LIMIT 1),''),
  coalesce((SELECT error FROM input_deliveries d WHERE d.prompt_id=turns.id ORDER BY acceptance_sequence DESC LIMIT 1),'')
 FROM turns WHERE project_id=? AND session_id=? AND `+queued+` AND sequence>? ORDER BY sequence LIMIT ?`, scope.ProjectID, session, observed, observedSource, after, limit+1)
	if err != nil {
		return PromptQueuePage{}, err
	}
	defer rows.Close()
	items := make([]Turn, 0, limit)
	var next int64
	hasMore := false
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.SessionID, &t.RequestID, &t.Content, &t.InitiallyHeld, &t.PromptRevision, &t.QueuePolicy, &t.Disposition, &t.State, &t.Sequence, &t.CreatedAt, &t.UpdatedAt, &t.DispatchError, &t.DeliveryID, &t.DeliveryState, &t.DeliveryError); err != nil {
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
