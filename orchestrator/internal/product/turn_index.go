package product

import (
	"context"
	"fmt"
	"slices"
)

// A bounded navigation index is independent of the currently rendered message page.
type TurnIndexItem struct {
	ID               string `json:"id"`
	Sequence         int64  `json:"sequence"`
	State            string `json:"state"`
	CreatedAt        string `json:"created_at"`
	UserPreview      string `json:"user_preview"`
	AssistantPreview string `json:"assistant_preview"`
}
type TurnIndexPage struct {
	Turns      []TurnIndexItem `json:"turns"`
	NextBefore int64           `json:"next_before,omitempty"`
	NextAfter  int64           `json:"next_after,omitempty"`
}

func (s *Store) TurnIndex(ctx context.Context, scope Scope, session string, before, after int64, limit int) (TurnIndexPage, error) {
	out := TurnIndexPage{Turns: []TurnIndexItem{}}
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return out, err
	}
	if limit < 1 || limit > 300 {
		limit = 100
	}
	if before < 0 || after < 0 || (before != 0 && after != 0) {
		return out, ErrInvalid
	}
	where, order := "", "DESC"
	args := []any{session}
	if before != 0 {
		where = " AND t.sequence<?"
		args = append(args, before)
	}
	if after != 0 {
		where = " AND t.sequence>?"
		order = "ASC"
		args = append(args, after)
	}
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, fmt.Sprintf(`SELECT t.id,t.sequence,t.state,t.created_at,substr(t.content,1,600),CASE WHEN t.state='completed' THEN coalesce((SELECT substr(m.content,1,1200) FROM messages m WHERE m.session_id=t.session_id AND m.turn_id=t.id AND m.role='assistant' ORDER BY m.sequence DESC LIMIT 1),'') ELSE '' END FROM turns t WHERE t.session_id=?%s ORDER BY t.sequence %s LIMIT ?`, where, order), args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item TurnIndexItem
		if err = rows.Scan(&item.ID, &item.Sequence, &item.State, &item.CreatedAt, &item.UserPreview, &item.AssistantPreview); err != nil {
			return out, err
		}
		out.Turns = append(out.Turns, item)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if after == 0 {
		slices.Reverse(out.Turns)
	}
	if len(out.Turns) > 0 {
		first, last := out.Turns[0].Sequence, out.Turns[len(out.Turns)-1].Sequence
		var older, newer bool
		err = s.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turns WHERE session_id=? AND sequence<?),EXISTS(SELECT 1 FROM turns WHERE session_id=? AND sequence>?)`, session, first, session, last).Scan(&older, &newer)
		if err != nil {
			return out, err
		}
		if older {
			out.NextBefore = first
		}
		if newer {
			out.NextAfter = last
		}
	}
	return out, nil
}
