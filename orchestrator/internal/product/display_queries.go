package product

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"
)

var ErrResync = errors.New("conversation view requires resync")

const displayResponseBytes = 256 << 10

type AgentRecoveryStatus struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Cleanup  string `json:"cleanup"`
	Recovery string `json:"recovery"`
	Error    string `json:"error,omitempty"`
}

type ViewHead struct {
	ActiveRun         *ExecutionRun         `json:"active_run,omitempty"`
	Session           Session               `json:"session"`
	ControlRevision   int64                 `json:"control_revision,string"`
	ContentRevision   int64                 `json:"content_revision,string"`
	IndexRevision     int64                 `json:"index_revision,string"`
	Generation        string                `json:"projection_generation"`
	State             string                `json:"projection_state"`
	SourceFrontier    string                `json:"source_frontier"`
	ProjectedFrontier string                `json:"projected_frontier"`
	Turns             []Turn                `json:"turns"`
	HasMoreTurns      bool                  `json:"has_more_turns"`
	HasContent        bool                  `json:"has_content"`
	Recovery          []AgentRecoveryStatus `json:"recovery"`
	Error             string                `json:"error,omitempty"`
}

func liveDisplaySession(ctx context.Context, q queryer, scope Scope, id string) (Session, error) {
	p, err := authorizedProject(ctx, q, scope)
	if err != nil {
		return Session{}, err
	}
	if p.State == "deleting" || p.State == "deleted" {
		return Session{}, ErrNotFound
	}
	return scanSession(q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE project_id=? AND id=? AND deletion_intent=0`, scope.ProjectID, id))
}

type ViewPage struct {
	Head      ViewHead      `json:"head"`
	Items     []DisplayItem `json:"items"`
	Summaries []TurnSummary `json:"turn_summaries"`
	Before    string        `json:"before,omitempty"`
	After     string        `json:"after,omitempty"`
	Window    string        `json:"window"`
}
type viewToken struct {
	Kind       string `json:"k"`
	Session    string `json:"s"`
	Generation string `json:"g"`
	Turn       int64  `json:"t,string,omitempty"`
	Position   int64  `json:"p,string,omitempty"`
	Block      int    `json:"b,omitempty"`
	ID         string `json:"i,omitempty"`
	Base       int64  `json:"a,string,omitempty"`
	Target     int64  `json:"z,string,omitempty"`
	Revision   int64  `json:"r,string,omitempty"`
	Ordinal    int    `json:"o,omitempty"`
	Offset     int    `json:"f,omitempty"`
}

func encodeViewToken(t viewToken) string {
	raw, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeViewToken(raw, kind, session, generation string) (viewToken, error) {
	var t viewToken
	if len(raw) > 4096 {
		return t, ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return t, ErrInvalid
	}
	if json.Unmarshal(b, &t) != nil || t.Kind != kind || t.Session != session {
		return t, ErrInvalid
	}
	if t.Generation != generation {
		return t, ErrResync
	}
	if t.Turn < 0 || t.Position < 0 || t.Block < 0 || t.Base < 0 || t.Target < 0 || t.Revision < 0 || t.Ordinal < 0 || t.Offset < 0 {
		return t, ErrInvalid
	}
	return t, nil
}
func frontier(ctx context.Context, q queryer, session string, at int64, latest bool) (string, error) {
	var incarnation string
	var sequence int64
	query := `SELECT incarnation_id,sequence FROM conversation_sources WHERE session_id=? AND source_id=?`
	args := []any{session, at}
	if latest {
		query = `SELECT incarnation_id,sequence FROM conversation_sources WHERE session_id=? ORDER BY source_id DESC LIMIT 1`
		args = []any{session}
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&incarnation, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal([]string{incarnation, strconv.FormatInt(sequence, 10)})
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func viewHead(ctx context.Context, q queryer, scope Scope, session string) (ViewHead, error) {
	var out ViewHead
	var err error
	out.Session, err = liveDisplaySession(ctx, q, scope, session)
	if err != nil {
		return out, err
	}
	out.ControlRevision = out.Session.ControlRevision
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_items WHERE session_id=?)`, session).Scan(&out.HasContent); err != nil {
		return out, err
	}
	out.Turns = []Turn{}
	p, err := checkpoint(ctx, q, session)
	if errors.Is(err, sql.ErrNoRows) {
		p.State = "building"
		err = nil
	}
	if err != nil {
		return out, err
	}
	out.Generation = p.Generation
	out.ContentRevision = p.Revision
	out.IndexRevision = p.Index
	out.State = p.State
	out.Error = p.Error
	out.SourceFrontier, err = frontier(ctx, q, session, 0, true)
	if err != nil {
		return out, err
	}
	out.ProjectedFrontier, err = frontier(ctx, q, session, p.Source, false)
	if err != nil {
		return out, err
	}
	if out.State == "ready" && out.SourceFrontier != out.ProjectedFrontier {
		out.State = "catching_up"
	}
	if out.State == "ready" {
		var pending bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_pending_sessions WHERE session_id=?)`, session).Scan(&pending); err != nil {
			return out, err
		}
		if pending {
			out.State = "catching_up"
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id=? AND (disposition IN ('pending','claimed') OR EXISTS(SELECT 1 FROM execution_runs r WHERE r.prompt_id=turns.id AND r.state IN ('starting','running','stopping','unknown') AND r.acknowledged=0)) ORDER BY CASE WHEN EXISTS(SELECT 1 FROM execution_runs r WHERE r.prompt_id=turns.id AND r.state IN ('starting','running','stopping','unknown') AND r.acknowledged=0) THEN 0 ELSE 1 END,sequence DESC LIMIT 33`, session)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		t, e := scanTurn(rows)
		if e != nil {
			return out, e
		}
		if len(out.Turns) == 32 {
			out.HasMoreTurns = true
			break
		}
		t.Content = clipped(t.Content, 256)
		out.Turns = append(out.Turns, t)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	var run ExecutionRun
	err = q.QueryRowContext(ctx, `SELECT id,prompt_id,incarnation_id,owner,state FROM execution_runs WHERE session_id=? AND (state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0))`, session).Scan(&run.ID, &run.PromptID, &run.IncarnationID, &run.Owner, &run.State)
	if err == nil {
		out.ActiveRun = &run
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Recovery = []AgentRecoveryStatus{}
	rows, err = q.QueryContext(ctx, `SELECT id,state,cleanup_state,recovery_state,error FROM agent_incarnations WHERE session_id=? ORDER BY created_at DESC LIMIT 8`, session)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var recovery AgentRecoveryStatus
		if err = rows.Scan(&recovery.ID, &recovery.State, &recovery.Cleanup, &recovery.Recovery, &recovery.Error); err != nil {
			return out, err
		}
		recovery.Error = clipped(recovery.Error, 1024)
		out.Recovery = append(out.Recovery, recovery)
	}
	return out, rows.Err()
}
func (s *Store) ConversationHead(ctx context.Context, scope Scope, session string) (ViewHead, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ViewHead{}, err
	}
	defer tx.Rollback()
	return viewHead(ctx, tx, scope, session)
}

type ViewSelector struct{ Before, After, AroundTurn, AroundMessage, AroundItem string }

func (s *Store) ConversationPage(ctx context.Context, scope Scope, session string, selector ViewSelector) (ViewPage, error) {
	out := ViewPage{Items: []DisplayItem{}, Summaries: []TurnSummary{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.Head, err = viewHead(ctx, tx, scope, session)
	if err != nil {
		return out, err
	}
	choices := 0
	for _, v := range []string{selector.Before, selector.After, selector.AroundTurn, selector.AroundMessage, selector.AroundItem} {
		if v != "" {
			choices++
		}
	}
	if choices > 1 {
		return out, ErrInvalid
	}
	for _, token := range []string{selector.Before, selector.After} {
		if token != "" {
			if _, e := decodeViewToken(token, "page", session, out.Head.Generation); e != nil {
				return out, e
			}
		}
	}
	if out.Head.Generation == "" || out.Head.State == "building" {
		return out, ErrPending
	}
	if out.Head.State == "failed" {
		return out, ErrCapacity
	}
	args := []any{session}
	where := ""
	direction := "DESC"
	if selector.Before != "" || selector.After != "" {
		token := selector.Before
		op := "<"
		if selector.After != "" {
			token = selector.After
			op = ">"
			direction = "ASC"
		}
		at, e := decodeViewToken(token, "page", session, out.Head.Generation)
		if e != nil {
			return out, e
		}
		where = ` AND (position,block_index,id)` + op + `(?,?,?)`
		args = append(args, at.Position, at.Block, at.ID)
	} else if selector.AroundTurn != "" || selector.AroundMessage != "" || selector.AroundItem != "" {
		column, value := "turn_id", selector.AroundTurn
		if selector.AroundMessage != "" {
			column, value = "message_id", selector.AroundMessage
		}
		if selector.AroundItem != "" {
			column, value = "id", selector.AroundItem
		}
		var turn, position int64
		var block int
		var id string
		if err = tx.QueryRowContext(ctx, `SELECT turn_sequence,position,block_index,id FROM conversation_items WHERE session_id=? AND `+column+`=? ORDER BY position,block_index,id LIMIT 1`, session, value).Scan(&turn, &position, &block, &id); err != nil {
			if errors.Is(err, sql.ErrNoRows) && out.Head.State == "catching_up" {
				return out, ErrPending
			}
			return out, missing(err)
		}
		where = ` AND (position,block_index,id)>=(?,?,?)`
		args = append(args, position, block, id)
		direction = "ASC"
	}
	rows, err := tx.QueryContext(ctx, `SELECT data FROM conversation_items WHERE session_id=?`+where+` ORDER BY position `+direction+`,block_index `+direction+`,id `+direction+` LIMIT 50`, args...)
	if err != nil {
		return out, err
	}
	// Reserve room for cursors and bounded summaries. Test the complete envelope
	// before returning; even the first item must fit the declared JSON byte cap.
	usedRaw, _ := json.Marshal(out)
	used := len(usedRaw) + (32 << 10)
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if used+len(raw) > displayResponseBytes {
			break
		}
		var item DisplayItem
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			break
		}
		out.Items = append(out.Items, item)
		used += len(raw) + 1
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if direction == "DESC" {
		slices.Reverse(out.Items)
	}
	seen := map[string]bool{}
	for _, item := range out.Items {
		if seen[item.TurnID] {
			continue
		}
		seen[item.TurnID] = true
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT data FROM conversation_turns WHERE session_id=? AND turn_id=?`, session, item.TurnID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		var summary TurnSummary
		if err = json.Unmarshal([]byte(raw), &summary); err != nil {
			return out, err
		}
		out.Summaries = append(out.Summaries, summary)
	}
	if len(out.Items) > 0 {
		first, last := out.Items[0], out.Items[len(out.Items)-1]
		token := func(item DisplayItem) string {
			return encodeViewToken(viewToken{Kind: "page", Session: session, Generation: out.Head.Generation, Turn: item.TurnSequence, Position: item.Position, Block: item.Block, ID: item.ID})
		}
		var earlier, later bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_items WHERE session_id=? AND (position,block_index,id)<(?,?,?)),EXISTS(SELECT 1 FROM conversation_items WHERE session_id=? AND (position,block_index,id)>(?,?,?))`, session, first.Position, first.Block, first.ID, session, last.Position, last.Block, last.ID).Scan(&earlier, &later); err != nil {
			return out, err
		}
		if earlier {
			out.Before = token(first)
		}
		if later {
			out.After = token(last)
		}
		out.Window = encodeViewToken(viewToken{Kind: "window", Session: session, Generation: out.Head.Generation, ID: first.ID, Revision: out.Head.ContentRevision})
	}
	encoded, _ := json.Marshal(out)
	if len(encoded) > displayResponseBytes {
		return ViewPage{}, ErrCapacity
	}
	return out, nil
}

type ViewChange struct {
	Revision  int64           `json:"revision,string"`
	Ordinal   int             `json:"ordinal"`
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	Item      json.RawMessage `json:"item,omitempty"`
}
type ViewChanges struct {
	Generation string       `json:"projection_generation"`
	Base       int64        `json:"base_revision,string"`
	Target     int64        `json:"target_revision,string"`
	Through    int64        `json:"processed_through,string"`
	Changes    []ViewChange `json:"changes"`
	Cursor     string       `json:"cursor,omitempty"`
	More       bool         `json:"has_more"`
}

func (s *Store) ConversationChanges(ctx context.Context, scope Scope, session string, base int64, cursor string) (ViewChanges, error) {
	out := ViewChanges{Base: base, Through: base, Changes: []ViewChange{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = liveDisplaySession(ctx, tx, scope, session); err != nil {
		return out, err
	}
	p, err := checkpoint(ctx, tx, session)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrPending
	}
	if err != nil {
		return out, err
	}
	out.Generation = p.Generation
	out.Target = p.Revision
	if base < 0 {
		return out, ErrInvalid
	}
	if base > p.Revision {
		return out, ErrResync
	}
	at := viewToken{Kind: "changes", Session: session, Generation: p.Generation, Base: base, Target: p.Revision, Revision: base, Ordinal: 2147483647}
	if cursor != "" {
		at, err = decodeViewToken(cursor, "changes", session, p.Generation)
		if err != nil {
			return out, err
		}
		if at.Base != base || at.Target > p.Revision || at.Revision < base || at.Revision > at.Target {
			return out, ErrInvalid
		}
		out.Target = at.Target
	}
	var pruned int64
	var ordinal int
	if err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT pruned_revision FROM conversation_retention WHERE session_id=?),0),coalesce((SELECT pruned_ordinal FROM conversation_retention WHERE session_id=?),-1)`, session, session).Scan(&pruned, &ordinal); err != nil {
		return out, err
	}
	if at.Revision < pruned || at.Revision == pruned && at.Ordinal < ordinal {
		return out, ErrResync
	}
	rows, err := tx.QueryContext(ctx, `SELECT revision,ordinal,item_id,operation,data FROM conversation_changes WHERE session_id=? AND (revision,ordinal)>(?,?) AND revision<=? ORDER BY revision,ordinal LIMIT 101`, session, at.Revision, at.Ordinal, out.Target)
	if err != nil {
		return out, err
	}
	used := 4096
	for rows.Next() {
		var c ViewChange
		var data string
		if err = rows.Scan(&c.Revision, &c.Ordinal, &c.ID, &c.Operation, &data); err != nil {
			break
		}
		if c.Operation == "upsert" {
			c.Item = json.RawMessage(data)
		}
		raw, _ := json.Marshal(c)
		if len(out.Changes) == 100 || used+len(raw) > displayResponseBytes {
			out.More = true
			break
		}
		out.Changes = append(out.Changes, c)
		used += len(raw) + 1
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if out.More {
		if len(out.Changes) == 0 {
			return out, ErrCapacity
		}
		last := out.Changes[len(out.Changes)-1]
		at.Revision = last.Revision
		at.Ordinal = last.Ordinal
		out.Through = last.Revision - 1 // A partial revision cannot be acknowledged yet.
		out.Cursor = encodeViewToken(at)
	} else {
		out.Through = out.Target
	}
	return out, nil
}

type ViewDetail struct {
	Version string `json:"version"`
	Data    string `json:"data"`
	Next    string `json:"next,omitempty"`
}

func (s *Store) ConversationDetail(ctx context.Context, scope Scope, session, item, version, cursor string) (ViewDetail, error) {
	out := ViewDetail{Version: version}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = liveDisplaySession(ctx, tx, scope, session); err != nil {
		return out, err
	}
	revision, err := strconv.ParseInt(version, 10, 64)
	if err != nil || revision < 1 {
		return out, ErrInvalid
	}
	p, err := checkpoint(ctx, tx, session)
	if err != nil {
		return out, missing(err)
	}
	at := viewToken{Kind: "detail", Session: session, Generation: p.Generation, ID: item, Revision: revision}
	if cursor != "" {
		at, err = decodeViewToken(cursor, "detail", session, p.Generation)
		if err != nil {
			return out, err
		}
		if at.ID != item || at.Revision != revision {
			return out, ErrInvalid
		}
	}
	var content []byte
	var total int
	err = tx.QueryRowContext(ctx, `SELECT substr(CAST(content AS BLOB),?,?),length(CAST(content AS BLOB)) FROM conversation_details WHERE session_id=? AND item_id=? AND item_revision=?`, at.Offset+1, 32<<10, session, item, revision).Scan(&content, &total)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT substr(CAST(data AS BLOB),?,?),length(CAST(data AS BLOB)) FROM conversation_changes WHERE session_id=? AND item_id=? AND revision=? AND operation='upsert' LIMIT 1`, at.Offset+1, 32<<10, session, item, revision).Scan(&content, &total)
	}
	if err != nil {
		return out, missing(err)
	}
	if at.Offset > total || len(content) > 0 && !utf8.RuneStart(content[0]) {
		return out, ErrInvalid
	}
	end := len(content)
	for end > 0 && !utf8.Valid(content[:end]) {
		end--
	}
	out.Data = string(content[:end])
	if at.Offset+end < total {
		if end == 0 {
			return out, ErrInvalid
		}
		at.Offset += end
		out.Next = encodeViewToken(at)
	}
	return out, nil
}

// Rebuild changes only derived data and generation. It cannot replay commands.
func (s *Store) RebuildConversation(ctx context.Context, scope Scope, session string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := liveDisplaySession(ctx, tx, scope, session); err != nil {
			return err
		}
		for _, table := range []string{"conversation_segment_inputs", "conversation_segments", "conversation_details", "conversation_changes", "conversation_retention", "conversation_items", "conversation_detail_gc", "conversation_turns", "conversation_checkpoints", "conversation_message_groups"} {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE session_id=?", table), session); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_dirty_turns SELECT session_id,id FROM turns WHERE session_id=?`, session); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_pending_sessions VALUES(?)`, session)
		return err
	})
}
