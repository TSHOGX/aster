package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

const DefaultMessagePageSize = 40
const MaxMessagePageSize = 80
const MessagePageBytes = 256 * 1024

type MessagePageParams struct {
	SessionID, UserID           string
	Summary                     bool
	MaxID                       *int64
	BeforeID, AfterID, AroundID int64
	Limit                       int
}
type MessagePage struct {
	Messages      []Message `json:"messages"`
	HasOlder      bool      `json:"has_older"`
	HasNewer      bool      `json:"has_newer"`
	BeforeID      int64     `json:"before_id"`
	AfterID       int64     `json:"after_id"`
	LatestID      int64     `json:"latest_id"`
	LatestFinalID int64     `json:"latest_final_id"`
	Interruption  string    `json:"interruption"`
}
type ContentPage struct {
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	Total      int    `json:"total"`
	HasMore    bool   `json:"has_more"`
}
type TracePage struct {
	Trace      []json.RawMessage `json:"trace"`
	NextOffset int               `json:"next_offset"`
	HasMore    bool              `json:"has_more"`
}

func (p MessagePageParams) predicate(alias string) (string, []any) {
	w := alias + ".session_id = ?"
	a := []any{p.SessionID}
	if p.MaxID != nil {
		w += " AND " + alias + ".id <= ?"
		a = append(a, *p.MaxID)
	}
	if p.Summary {
		w += " AND (" + alias + ".role = 'user' OR " + alias + ".stop_reason <> 'toolUse')"
	}
	return w, a
}

// Keep the same fallback as the conversation: an unknown terminator is final
// only when the actual next visible row is a user, or no such row exists.
func finalSQL(p MessagePageParams) (string, []any) {
	w, a := p.predicate("n")
	return `(m.role = 'assistant' AND (m.stop_reason = 'stop' OR (m.stop_reason NOT IN ('toolUse','error','sandbox_died') AND COALESCE((SELECT n.role FROM messages n WHERE ` + w + ` AND n.id>m.id ORDER BY n.id LIMIT 1),'user') = 'user')))`, a
}
func (s *Store) ListMessagesPage(ctx context.Context, p MessagePageParams) (MessagePage, error) {
	out := MessagePage{Messages: []Message{}}
	if p.Limit <= 0 {
		p.Limit = DefaultMessagePageSize
	}
	if p.Limit > MaxMessagePageSize {
		p.Limit = MaxMessagePageSize
	}
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	w, a := p.predicate("m")
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(m.id),0) FROM messages m WHERE `+w, a...).Scan(&out.LatestID); err != nil {
		return out, err
	}
	f, fa := finalSQL(p)
	args := append(append([]any{}, a...), fa...)
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(m.id),0) FROM messages m WHERE `+w+` AND `+f, args...).Scan(&out.LatestFinalID); err != nil {
		return out, err
	}
	// Only roles and terminators of the latest turn are needed; no historical body
	// or trace is pulled into the tail-state calculation.
	tailArgs := append(append([]any{}, a...), a...)
	rows, err := tx.QueryContext(ctx, `SELECT m.role,m.stop_reason FROM messages m WHERE `+w+` AND m.id>COALESCE((SELECT MAX(m.id) FROM messages m WHERE `+w+` AND m.role='user'),0) ORDER BY m.id`, tailArgs...)
	if err != nil {
		return out, err
	}
	completed := false
	for rows.Next() {
		var role, reason string
		if err = rows.Scan(&role, &reason); err != nil {
			rows.Close()
			return out, err
		}
		if role != "assistant" {
			continue
		}
		switch reason {
		case "stop":
			completed = true
			out.Interruption = ""
		case "sandbox_died":
			if !completed {
				out.Interruption = "sandbox"
			}
		case "error", "toolUse":
			if !completed {
				out.Interruption = "model"
			}
		default:
			out.Interruption = ""
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	read := func(extra, order string, limit int, cursor int64) ([]Message, error) {
		args := append([]any{}, fa...)
		args = append(args, p.UserID)
		args = append(args, a...)
		if extra != "" {
			args = append(args, cursor)
		}
		args = append(args, limit)
		query := `SELECT m.id,m.session_id,m.turn_id,m.role,CASE WHEN length(m.content)>4096 THEN substr(m.content,1,2048) ELSE m.content END,m.created_at,m.stop_reason,COALESCE(f.rating,''),length(m.content),EXISTS(SELECT 1 FROM message_traces t WHERE t.message_id=m.id),` + f + ` FROM messages m LEFT JOIN message_feedback f ON f.message_id=m.id AND f.user_id=? WHERE ` + w + extra + ` ORDER BY m.id ` + order + ` LIMIT ?`
		// The final-reply expression occurs after the SELECT fields and before JOIN.
		r, e := tx.QueryContext(ctx, query, args...)
		if e != nil {
			return nil, e
		}
		defer r.Close()
		items := []Message{}
		for r.Next() {
			var m Message
			var t sql.NullInt64
			var created string
			var final bool
			if e = r.Scan(&m.ID, &m.SessionID, &t, &m.Role, &m.Content, &created, &m.StopReason, &m.Feedback, &m.ContentLength, &m.HasTrace, &final); e != nil {
				return nil, e
			}
			if t.Valid {
				m.TurnID = &t.Int64
			}
			m.CreatedAt = parseTime(created)
			m.ContentTruncated = m.ContentLength > 4096
			// SQLite length(text) stops at NUL; guard the actual returned string
			// too so unusual stored text cannot bypass the preview budget.
			if len(m.Content) > 16384 {
				runes := []rune(m.Content)
				if len(runes) > 4096 {
					m.ContentLength = len(runes)
					m.Content = string(runes[:2048])
					m.ContentTruncated = true
				}
			}
			m.IsFinalReply = &final
			if p.Summary {
				m.HasTrace = false
			}
			items = append(items, m)
		}
		return items, r.Err()
	}
	var items []Message
	switch {
	case p.AroundID > 0:
		var exists bool
		args := append(append([]any{}, a...), p.AroundID)
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m WHERE `+w+` AND m.id=?)`, args...).Scan(&exists); err != nil {
			return out, err
		}
		if !exists {
			return out, sql.ErrNoRows
		}
		left, e := read(" AND m.id<=?", "DESC", p.Limit/2+1, p.AroundID)
		if e != nil {
			return out, e
		}
		right, e := read(" AND m.id>?", "ASC", p.Limit-len(left), p.AroundID)
		if e != nil {
			return out, e
		}
		items = append(left, right...)
	case p.AfterID > 0:
		items, err = read(" AND m.id>?", "ASC", p.Limit, p.AfterID)
	case p.BeforeID > 0:
		items, err = read(" AND m.id<?", "DESC", p.Limit, p.BeforeID)
	default:
		items, err = read("", "DESC", p.Limit, 0)
	}
	if err != nil {
		return out, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	// Bound encoded bytes, including JSON escaping; trim from the end farthest
	// from the requested anchor, never dropping an around target.
	for len(items) > 1 {
		b, e := json.Marshal(items)
		if e != nil {
			return out, e
		}
		if len(b) <= MessagePageBytes {
			break
		}
		dropFirst := p.AfterID == 0
		if p.AroundID > 0 {
			dropFirst = p.AroundID-items[0].ID > items[len(items)-1].ID-p.AroundID
		}
		if dropFirst {
			items = items[1:]
		} else {
			items = items[:len(items)-1]
		}
	}
	out.Messages = items
	older, newer := p.BeforeID, p.AfterID
	if len(items) > 0 {
		older = items[0].ID
		newer = items[len(items)-1].ID
		out.BeforeID = older
		out.AfterID = newer
	}
	if older > 0 {
		args := append(append([]any{}, a...), older)
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m WHERE `+w+` AND m.id<?)`, args...).Scan(&out.HasOlder)
		if err != nil {
			return out, err
		}
	}
	if newer > 0 {
		args := append(append([]any{}, a...), newer)
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m WHERE `+w+` AND m.id>?)`, args...).Scan(&out.HasNewer)
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
func (s *Store) MessageContent(ctx context.Context, p MessagePageParams, id int64, offset, limit int) (ContentPage, error) {
	out := ContentPage{Offset: offset}
	if offset < 0 || limit < 1 || limit > 8192 {
		return out, fmt.Errorf("invalid content range")
	}
	w, a := p.predicate("m")
	args := []any{offset + 1, limit}
	args = append(args, a...)
	args = append(args, id)
	err := s.reader().QueryRowContext(ctx, `SELECT substr(m.content,?,?),length(m.content) FROM messages m WHERE `+w+` AND m.id=?`, args...).Scan(&out.Content, &out.Total)
	out.NextOffset = offset + len([]rune(out.Content))
	out.HasMore = out.NextOffset < out.Total
	return out, err
}
func (s *Store) MessageTrace(ctx context.Context, p MessagePageParams, id int64, offset, limit int) (TracePage, error) {
	out := TracePage{Trace: []json.RawMessage{}, NextOffset: offset}
	if p.Summary {
		return out, sql.ErrNoRows
	}
	if offset < 0 || limit < 1 || limit > 50 {
		return out, fmt.Errorf("invalid trace range")
	}
	w, a := p.predicate("m")
	args := append(append([]any{}, a...), id)
	var found bool
	if err := s.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m WHERE `+w+` AND m.id=?)`, args...).Scan(&found); err != nil {
		return out, err
	}
	if !found {
		return out, sql.ErrNoRows
	}
	args = append(args, offset, limit+1)
	rows, err := s.reader().QueryContext(ctx, `SELECT j.value FROM messages m JOIN message_traces t ON t.message_id=m.id, json_each(t.nodes) j WHERE `+w+` AND m.id=? AND CAST(j.key AS INTEGER)>=? ORDER BY CAST(j.key AS INTEGER) LIMIT ?`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	size := 2
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if len(raw) > 8192 {
			preview, _ := json.Marshal(map[string]any{"kind": "tool", "label": "Large execution detail", "detail_index": out.NextOffset})
			raw = string(preview)
		}
		if len(out.Trace) >= limit || size+len(raw)+1 > MessagePageBytes {
			out.HasMore = true
			break
		}
		out.Trace = append(out.Trace, json.RawMessage(raw))
		out.NextOffset++
		size += len(raw) + 1
	}
	return out, rows.Err()
}

// TraceContent returns the raw JSON of one large node in bounded character
// ranges. It uses the identical visibility predicate as the trace list.
func (s *Store) TraceContent(ctx context.Context, p MessagePageParams, id int64, node, offset, limit int) (ContentPage, error) {
	out := ContentPage{Offset: offset}
	if p.Summary {
		return out, sql.ErrNoRows
	}
	if node < 0 || offset < 0 || limit < 1 || limit > 8192 {
		return out, fmt.Errorf("invalid trace range")
	}
	w, a := p.predicate("m")
	args := []any{fmt.Sprintf("$[%d]", node)}
	args = append(args, a...)
	args = append(args, id, offset+1, limit)
	err := s.reader().QueryRowContext(ctx, `WITH detail AS (SELECT json_extract(t.nodes,?) AS content FROM messages m JOIN message_traces t ON t.message_id=m.id WHERE `+w+` AND m.id=?) SELECT substr(content,?,?),length(content) FROM detail WHERE content IS NOT NULL`, args...).Scan(&out.Content, &out.Total)
	out.NextOffset = offset + len([]rune(out.Content))
	out.HasMore = out.NextOffset < out.Total
	return out, err
}
