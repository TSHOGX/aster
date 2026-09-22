package product

import (
	"context"
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
}

func (s *Store) TurnIndex(ctx context.Context, scope Scope, session string, before int64, limit int) (TurnIndexPage, error) {
	out := TurnIndexPage{Turns: []TurnIndexItem{}}
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return out, err
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT t.id,t.sequence,t.state,t.created_at,substr(t.content,1,600),CASE WHEN t.state='completed' THEN coalesce((SELECT substr(m.content,1,1200) FROM messages m WHERE m.session_id=t.session_id AND m.turn_id=t.id AND m.role='assistant' ORDER BY m.sequence DESC LIMIT 1),'') ELSE '' END FROM turns t WHERE t.session_id=? AND (?=0 OR t.sequence<?) ORDER BY t.sequence DESC LIMIT ?`, session, before, before, limit+1)
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
	if len(out.Turns) > limit {
		out.Turns = out.Turns[:limit]
		out.NextBefore = out.Turns[len(out.Turns)-1].Sequence
	}
	slices.Reverse(out.Turns)
	return out, rows.Err()
}
