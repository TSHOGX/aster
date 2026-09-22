package product

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

type AgentRecoveryStatus struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Cleanup  string `json:"cleanup"`
	Recovery string `json:"recovery"`
	Error    string `json:"error,omitempty"`
}
type SessionSnapshot struct {
	TurnSummaries     []TurnSummary         `json:"turn_summaries"`
	Timeline          []TimelineItem        `json:"timeline"`
	HasLater          bool                  `json:"has_later"`
	TimelineTruncated bool                  `json:"timeline_truncated"`
	Recovery          []AgentRecoveryStatus `json:"recovery"`
	Records           []AgentRecord         `json:"records,omitempty"`

	Session      Session   `json:"session"`
	Messages     []Message `json:"messages"`
	Turns        []Turn    `json:"turns"`
	RecordCursor int64     `json:"record_cursor"`
	HasEarlier   bool      `json:"has_earlier"`
	HasMoreTurns bool      `json:"has_more_turns"`
}

// Snapshot is one SQLite read transaction. Its own Session revision fences slow
// browser responses; Project/Terminal state is deliberately not copied into it.
func (s *Store) SessionSnapshot(ctx context.Context, scope Scope, session string, before int64, withRecords bool, target ...string) (SessionSnapshot, error) {
	anchor := ""
	if len(target) > 0 {
		anchor = target[0]
	}
	return s.SessionSnapshotPage(ctx, scope, session, before, 0, withRecords, anchor)
}
func (s *Store) SessionSnapshotPage(ctx context.Context, scope Scope, session string, before, after int64, withRecords bool, anchor string) (SessionSnapshot, error) {
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
	var selected *Turn
	targetID := ""
	var anchorSequence int64
	if anchor != "" {
		t, e := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id=? AND id=?`, session, anchor))
		if e != nil {
			return out, e
		}
		selected = &t
		targetID = t.ID
		if e = tx.QueryRowContext(ctx, `SELECT coalesce(min(sequence),0) FROM messages WHERE session_id=? AND turn_id=?`, session, t.ID).Scan(&anchorSequence); e != nil {
			return out, e
		}
		if anchorSequence > 0 {
			before = anchorSequence + 51
			after = 0
		}
	}
	out.Messages = []Message{}
	out.Turns = []Turn{}
	order := "DESC"
	if after > 0 {
		order = "ASC"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,session_id,turn_id,role,content,NULL,sequence,created_at FROM messages WHERE session_id=? AND (?=0 OR sequence<?) AND (?=0 OR sequence>?) ORDER BY sequence `+order+` LIMIT 101`, session, before, before, after, after)
	if err != nil {
		return out, err
	}
	messageBytes := 0
	for rows.Next() {
		var m Message
		var native []byte
		if err = rows.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.TurnID, &m.Role, &m.Content, &native, &m.Sequence, &m.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		if messageBytes+len(m.Content) > 2<<20 && len(out.Messages) > 0 {
			out.HasEarlier = true
			break
		}
		messageBytes += len(m.Content)
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
	if after == 0 {
		slices.Reverse(out.Messages)
	}
	if selected != nil {
		found := false
		for _, m := range out.Messages {
			if m.Role == "user" && m.TurnID == selected.ID {
				found = true
				break
			}
		}
		if !found {
			out.Messages = append([]Message{{ID: selected.ID + "/user", ProjectID: scope.ProjectID, SessionID: session, TurnID: selected.ID, Role: "user", Content: selected.Content, Sequence: anchorSequence, CreatedAt: selected.CreatedAt}}, out.Messages...)
		}
	}

	slices.SortFunc(out.Messages, func(a, b Message) int { return cmp.Compare(a.Sequence, b.Sequence) })
	if len(out.Messages) > 0 {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE session_id=? AND sequence<?),EXISTS(SELECT 1 FROM messages WHERE session_id=? AND sequence>?)`, session, out.Messages[0].Sequence, session, out.Messages[len(out.Messages)-1].Sequence).Scan(&out.HasEarlier, &out.HasLater); err != nil {
			return out, err
		}
	}

	rows, err = tx.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id=? ORDER BY CASE WHEN state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0) THEN 0 WHEN state IN ('queued','held') THEN 1 ELSE 2 END,CASE WHEN state IN ('queued','held') THEN sequence ELSE 0 END,sequence DESC LIMIT 101`, session)
	if err != nil {
		return out, err
	}
	turnBytes := 0
	for rows.Next() {
		turn, e := scanTurn(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		if turnBytes+len(turn.Content) > 2<<20 && len(out.Turns) > 0 {
			out.HasMoreTurns = true
			break
		}
		turnBytes += len(turn.Content)
		out.Turns = append(out.Turns, turn)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Turns) > 100 {
		out.HasMoreTurns = true
		out.Turns = out.Turns[:100]
	}
	if selected != nil {
		found := false
		for _, t := range out.Turns {
			if t.ID == selected.ID {
				found = true
				break
			}
		}
		if !found {
			out.Turns = append(out.Turns, *selected)
		}
	}
	slices.SortFunc(out.Turns, func(a, b Turn) int { return cmp.Compare(a.Sequence, b.Sequence) })
	err = tx.QueryRowContext(ctx, `SELECT coalesce(max(r.rowid),0) FROM agent_records r JOIN agent_incarnations i ON i.id=r.incarnation_id WHERE i.session_id=?`, session).Scan(&out.RecordCursor)
	if err != nil {
		return out, err
	}
	out.Recovery = []AgentRecoveryStatus{}
	rows, err = tx.QueryContext(ctx, `SELECT id,state,cleanup_state,recovery_state,error FROM agent_incarnations WHERE session_id=? ORDER BY created_at DESC LIMIT 8`, session)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var recovery AgentRecoveryStatus
		if err = rows.Scan(&recovery.ID, &recovery.State, &recovery.Cleanup, &recovery.Recovery, &recovery.Error); err != nil {
			rows.Close()
			return out, err
		}
		out.Recovery = append(out.Recovery, recovery)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if withRecords {
		out.Records, err = recentAgentRecords(ctx, tx, session)
		if err != nil {
			return out, err
		}
	}
	out.Timeline, out.TimelineTruncated, err = sessionTimeline(ctx, tx, session, out.Messages, out.Turns, before == 0 && after == 0 && targetID == "")
	if err != nil {
		return out, err
	}
	out.TurnSummaries, err = turnSummaries(ctx, tx, session, out.Messages, out.Turns)
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
	total := 0
	for rows.Next() {
		var r AgentRecord
		var payload []byte
		if err = rows.Scan(&r.Cursor, &r.TurnID, &r.Kind, &payload); err != nil {
			return nil, err
		}
		if total+len(payload) > 2<<20 && len(out) > 0 {
			break
		}
		total += len(payload)
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

func (s *Store) MessageSequence(ctx context.Context, scope Scope, session, id string) (int64, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return 0, err
	}
	var sequence int64
	err := missing(s.reader.QueryRowContext(ctx, `SELECT sequence FROM messages WHERE session_id=? AND id=?`, session, id).Scan(&sequence))
	return sequence, err
}

func recentAgentRecords(ctx context.Context, q queryer, session string) ([]AgentRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.rowid,r.turn_id,r.kind,r.payload FROM agent_records r JOIN agent_incarnations i ON i.id=r.incarnation_id WHERE i.session_id=? AND r.kind='native' AND json_extract(r.payload,'$.type') IN ('tool_execution_start','tool_execution_end') ORDER BY r.rowid DESC LIMIT 100`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentRecord{}
	total := 0
	for rows.Next() {
		var record AgentRecord
		var payload []byte
		if err = rows.Scan(&record.Cursor, &record.TurnID, &record.Kind, &payload); err != nil {
			return nil, err
		}
		if total+len(payload) > 2<<20 && len(out) > 0 {
			break
		}
		total += len(payload)
		record.Payload = payload
		out = append(out, record)
	}
	slices.Reverse(out)
	return out, rows.Err()
}
