package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
)

// EvolutionSourcePolicyRevision returns policy provenance, not a source-content
// grant. Historical policies never authorize capture or derivative publication.
func (s *Store) EvolutionSourcePolicyRevision(ctx context.Context, token, group, source string, revision int64) (evolution.SourceBinding, error) {
	var result evolution.SourceBinding
	if !validID(group) || !validID(source) || revision < 1 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT body FROM evolution_source_policy_revisions WHERE group_id=? AND source_id=? AND revision=?", group, source, revision).Scan(&raw); err != nil {
			return missing(err)
		}
		value, err := evolution.Decode[evolution.SourceBinding](bytes.NewBufferString(raw))
		if err != nil || value.ID != source || value.PolicyRevision != revision {
			return ErrInvalid
		}
		result = value
		return nil
	})
	return result, err
}
