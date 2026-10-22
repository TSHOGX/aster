package product

import (
	"context"
	"database/sql"
)

type EvolutionOperationEvent struct {
	Sequence  int64  `json:"sequence"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
}
type EvolutionEventPage struct {
	Events    []EvolutionOperationEvent `json:"events"`
	NextAfter int64                     `json:"next_after"`
	HasMore   bool                      `json:"has_more"`
}

func (s *Store) EvolutionOperationEvents(ctx context.Context, token, group, operation string, after int64, limit int) (EvolutionEventPage, error) {
	result := EvolutionEventPage{Events: []EvolutionOperationEvent{}, NextAfter: after}
	if !validID(group) || !validID(operation) || after < 0 || limit < 1 || limit > 100 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM evolution_operation_states WHERE group_id=? AND operation_id=?", group, operation).Scan(&exists); err != nil {
			return missing(err)
		}
		rows, err := tx.QueryContext(ctx, "SELECT sequence,state,created_at FROM evolution_operation_events WHERE operation_id=? AND sequence>? ORDER BY sequence LIMIT ?", operation, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event EvolutionOperationEvent
			if err = rows.Scan(&event.Sequence, &event.State, &event.CreatedAt); err != nil {
				return err
			}
			if len(result.Events) == limit {
				result.HasMore = true
				break
			}
			result.Events = append(result.Events, event)
			result.NextAfter = event.Sequence
		}
		return rows.Err()
	})
	return result, err
}
