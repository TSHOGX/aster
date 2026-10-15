package product

import (
	"context"
	"database/sql"
)

type EvolutionResource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
}
type EvolutionResourcePage struct {
	Items     []EvolutionResource `json:"items"`
	NextAfter string              `json:"next_after"`
	HasMore   bool                `json:"has_more"`
}

// EvolutionSourceResources lists only resources the current actor owns. Being a
// configuration manager or administrator never broadens this metadata query.
// Volume library metadata is resolved through the scoped Studio catalog instead.
func (s *Store) EvolutionSourceResources(ctx context.Context, token, group, kind, search, after string, limit int) (EvolutionResourcePage, error) {
	result := EvolutionResourcePage{Items: []EvolutionResource{}, NextAfter: after}
	if !validID(group) || kind != "project" && kind != "session" || len(search) > 256 || after != "" && !validID(after) || limit < 1 || limit > 50 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read")
		if err != nil {
			return err
		}
		var exists string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM account_groups WHERE id=?", group).Scan(&exists); err != nil {
			return missing(err)
		}
		query := `SELECT id,title,id FROM projects WHERE owner_id=? AND state NOT IN('deleting','deleted') AND id>? AND instr(lower(title||' '||id),lower(?))>0 ORDER BY id LIMIT ?`
		if kind == "session" {
			query = `SELECT s.id,s.title,p.id FROM sessions s JOIN projects p ON p.id=s.project_id WHERE p.owner_id=? AND p.state NOT IN('deleting','deleted') AND s.deletion_intent=0 AND s.id>? AND instr(lower(s.title||' '||s.id||' '||p.title),lower(?))>0 ORDER BY s.id LIMIT ?`
		}
		rows, err := tx.QueryContext(ctx, query, actor.ID, after, search, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EvolutionResource
			if err = rows.Scan(&item.ID, &item.Name, &item.ProjectID); err != nil {
				return err
			}
			if len(result.Items) == limit {
				result.HasMore = true
				break
			}
			result.Items = append(result.Items, item)
			result.NextAfter = item.ID
		}
		return rows.Err()
	})
	return result, err
}
