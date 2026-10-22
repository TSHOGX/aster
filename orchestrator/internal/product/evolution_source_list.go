package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
)

type EvolutionSourcePage struct {
	Sources   []evolution.SourceBinding `json:"sources"`
	NextAfter string                    `json:"next_after"`
	HasMore   bool                      `json:"has_more"`
}

// EvolutionSources lists explicit group policy declarations. It does not imply
// current source-content access; capture and activation recheck issuer authority.
func (s *Store) EvolutionSources(ctx context.Context, token, group, after string, limit int) (EvolutionSourcePage, error) {
	result := EvolutionSourcePage{Sources: []evolution.SourceBinding{}, NextAfter: after}
	if !validID(group) || (after != "" && !validID(after)) || limit < 1 || limit > 100 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT source_id,revision,body FROM evolution_source_policies WHERE group_id=? AND source_id>? ORDER BY source_id LIMIT ?", group, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, raw string
			var revision int64
			if err = rows.Scan(&id, &revision, &raw); err != nil {
				return err
			}
			if len(result.Sources) == limit {
				result.HasMore = true
				break
			}
			source, err := evolution.Decode[evolution.SourceBinding](bytes.NewBufferString(raw))
			if err != nil || source.ID != id || source.PolicyRevision != revision {
				return ErrInvalid
			}
			result.Sources = append(result.Sources, source)
			result.NextAfter = id
		}
		return rows.Err()
	})
	return result, err
}
