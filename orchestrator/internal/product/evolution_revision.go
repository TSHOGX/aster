package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
)

type EvolutionConfigurationRevision struct {
	OperationID   string                  `json:"operation_id"`
	Digest        string                  `json:"digest"`
	Configuration evolution.Configuration `json:"configuration"`
}

func (s *Store) EvolutionConfigurationRevision(ctx context.Context, token, group string, revision int64) (EvolutionConfigurationRevision, error) {
	var result EvolutionConfigurationRevision
	if !validID(group) || revision < 1 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		var body string
		if err := tx.QueryRowContext(ctx, "SELECT operation_id,digest,body FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", group, revision).Scan(&result.OperationID, &result.Digest, &body); err != nil {
			return missing(err)
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewBufferString(body))
		if err != nil || config.GroupID != group || config.Revision != revision || evolution.Digest([]byte(body)) != result.Digest {
			return ErrInvalid
		}
		result.Configuration = config
		return nil
	})
	return result, err
}
