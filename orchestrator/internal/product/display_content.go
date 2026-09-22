package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

type ViewSummaries struct {
	Generation string        `json:"projection_generation"`
	Revision   int64         `json:"index_revision,string"`
	Turns      []TurnSummary `json:"turn_summaries"`
}

func (s *Store) ConversationSummaries(ctx context.Context, scope Scope, session string, ids []string) (ViewSummaries, error) {
	out := ViewSummaries{Turns: []TurnSummary{}}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = liveDisplaySession(ctx, tx, scope, session); err != nil {
		return out, err
	}
	if len(ids) > 50 {
		return out, ErrInvalid
	}
	p, err := checkpoint(ctx, tx, session)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrPending
	}
	if err != nil {
		return out, err
	}
	out.Generation = p.Generation
	out.Revision = p.Index
	for _, id := range ids {
		if !validID(id) {
			return out, ErrInvalid
		}
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT data FROM conversation_turns WHERE session_id=? AND turn_id=?`, session, id).Scan(&raw)
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
		out.Turns = append(out.Turns, summary)
	}
	return out, nil
}

// A finalized message is immutable authoritative text. Bounded chunks support
// complete copying even when only part of its display blocks is mounted.
func (s *Store) ConversationMessageText(ctx context.Context, scope Scope, session, id, cursor string) (ViewDetail, error) {
	out := ViewDetail{Version: id}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = liveDisplaySession(ctx, tx, scope, session); err != nil {
		return out, err
	}
	at := viewToken{Kind: "message", Session: session, ID: id}
	if cursor != "" {
		at, err = decodeViewToken(cursor, "message", session, "")
		if err != nil {
			return out, err
		}
		if at.ID != id {
			return out, ErrInvalid
		}
	}
	var data []byte
	var total int
	err = tx.QueryRowContext(ctx, `SELECT substr(CAST(content AS BLOB),?,?),length(CAST(content AS BLOB)) FROM messages WHERE session_id=? AND id=?`, at.Offset+1, 32<<10, session, id).Scan(&data, &total)
	if err != nil {
		return out, missing(err)
	}
	if at.Offset > total || len(data) > 0 && !utf8.RuneStart(data[0]) {
		return out, ErrInvalid
	}
	end := len(data)
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	out.Data = string(data[:end])
	if at.Offset+end < total {
		if end == 0 {
			return out, ErrInvalid
		}
		at.Offset += end
		out.Next = encodeViewToken(at)
	}
	return out, nil
}
