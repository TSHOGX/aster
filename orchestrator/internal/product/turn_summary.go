package product

import (
	"context"
	"strings"
)

// Display metadata is independent of the bounded execution-control turn page.
// An older conversation page must not guess completion from Session idle state.
type TurnSummary struct {
	Sequence       int64  `json:"sequence"`
	ID             string `json:"id"`
	State          string `json:"state"`
	CreatedAt      string `json:"created_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
	FinalMessageID string `json:"final_message_id,omitempty"`
}

func turnSummaries(ctx context.Context, q queryer, session string, messages []Message, turns []Turn) ([]TurnSummary, error) {
	ids := map[string]bool{}
	for _, m := range messages {
		ids[m.TurnID] = true
	}
	for _, t := range turns {
		ids[t.ID] = true
	}
	delete(ids, "")
	out := []TurnSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{session}
	marks := []string{}
	for id := range ids {
		args = append(args, id)
		marks = append(marks, "?")
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id,t.sequence,t.state,t.created_at,t.updated_at,coalesce((SELECT m.id FROM messages m WHERE m.session_id=t.session_id AND m.turn_id=t.id AND m.role='assistant' ORDER BY m.sequence DESC LIMIT 1),'') FROM turns t WHERE t.session_id=? AND t.id IN (`+strings.Join(marks, ",")+`) ORDER BY t.sequence`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item TurnSummary
		var updated, final string
		if err = rows.Scan(&item.ID, &item.Sequence, &item.State, &item.CreatedAt, &updated, &final); err != nil {
			return nil, err
		}
		switch item.State {
		case "completed":
			item.FinishedAt = updated
			item.FinalMessageID = final
		case "failed", "canceled", "unknown":
			item.FinishedAt = updated
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
