package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

type SessionSnapshot struct {
	Session      Session   `json:"session"`
	Messages     []Message `json:"messages"`
	Turns        []Turn    `json:"turns"`
	RecordCursor int64     `json:"record_cursor"`
	HasEarlier   bool      `json:"has_earlier"`
}

// Snapshot is one SQLite read transaction. Its own Session revision fences slow
// browser responses; Project/Terminal state is deliberately not copied into it.
func (s *Store) SessionSnapshot(ctx context.Context, scope Scope, session string, before int64) (SessionSnapshot, error) {
	var out SessionSnapshot
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.Session, err = sessionIn(ctx, tx, scope, session)
	if err != nil {
		return out, err
	}
	out.Messages = []Message{}
	out.Turns = []Turn{}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,session_id,turn_id,role,content,native_message,sequence,created_at FROM messages WHERE session_id=? AND (?=0 OR sequence<?) ORDER BY sequence DESC LIMIT 101`, session, before, before)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m Message
		var native []byte
		if err = rows.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.TurnID, &m.Role, &m.Content, &native, &m.Sequence, &m.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		m.Native = native
		out.Messages = append(out.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Messages) > 100 {
		out.HasEarlier = true
		out.Messages = out.Messages[:100]
	}
	slices.Reverse(out.Messages)
	rows, err = tx.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id=? ORDER BY sequence DESC LIMIT 100`, session)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		turn, e := scanTurn(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Turns = append(out.Turns, turn)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	slices.Reverse(out.Turns)
	err = tx.QueryRowContext(ctx, `SELECT coalesce(max(r.rowid),0) FROM agent_records r JOIN agent_incarnations i ON i.id=r.incarnation_id WHERE i.session_id=?`, session).Scan(&out.RecordCursor)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type AgentRecord struct {
	Cursor  int64           `json:"cursor"`
	TurnID  string          `json:"turn_id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Store) AgentRecords(ctx context.Context, scope Scope, session string, after int64, limit int) ([]AgentRecord, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 200
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT r.rowid,r.turn_id,r.kind,r.payload FROM agent_records r JOIN agent_incarnations i ON i.id=r.incarnation_id WHERE i.session_id=? AND r.rowid>? ORDER BY r.rowid LIMIT ?`, session, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentRecord{}
	for rows.Next() {
		var r AgentRecord
		var payload []byte
		if err = rows.Scan(&r.Cursor, &r.TurnID, &r.Kind, &payload); err != nil {
			return nil, err
		}
		r.Payload = payload
		out = append(out, r)
	}
	return out, rows.Err()
}
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
		if child.Revision != revision {
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

func (s *Store) MessageSequence(ctx context.Context, scope Scope, session, id string) (int64, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return 0, err
	}
	var sequence int64
	err := missing(s.reader.QueryRowContext(ctx, `SELECT sequence FROM messages WHERE session_id=? AND id=?`, session, id).Scan(&sequence))
	return sequence, err
}
