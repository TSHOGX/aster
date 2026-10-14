package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"harness-platform/orchestrator/internal/evaluationclient"
)

// EvolutionProjection returns the immutable bytes' decoded configuration for an
// operation. Admission switches such as immediate disable do not rewrite it.
func (s *Store) EvolutionProjection(ctx context.Context, token, operation string) (evaluationclient.Projection, error) {
	var result evaluationclient.Projection
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.adminSession(ctx, tx, token); err != nil {
			return err
		}
		var err error
		result, err = readEvolutionProjection(ctx, tx, operation)
		return err
	})
	return result, err
}

// ReconcileEvolutionProjection records only the exact receipt verified by the
// participant client. The network call runs outside the database transaction.
func (s *Store) ReconcileEvolutionProjection(ctx context.Context, token, operation string, client *evaluationclient.Client) error {
	if client == nil {
		return ErrInvalid
	}
	projection, err := s.EvolutionProjection(ctx, token, operation)
	if err != nil {
		return err
	}
	if err := authorizeEvolutionSources(ctx, s.reader, projection.Configuration); err != nil {
		return err
	}
	allowed, err := s.AuthorizeEvolutionProjection(ctx, ProjectionScope{GroupID: projection.Configuration.GroupID, OperationID: operation, Revision: projection.Configuration.Revision, Digest: projection.Digest})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrConflict
	}
	receipt, err := client.Reconcile(ctx, projection)
	if err != nil {
		return err
	}
	return s.acceptEvolutionParticipant(ctx, token, receipt)
}

func readEvolutionProjection(ctx context.Context, q queryer, operation string) (evaluationclient.Projection, error) {
	var result evaluationclient.Projection
	var body, group string
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT group_id,revision,operation_id,digest,body FROM evolution_configuration_revisions WHERE operation_id=?`, operation).Scan(&group, &revision, &result.OperationID, &result.Digest, &body); err != nil {
		return result, err
	}
	config, err := evolution.Decode[evolution.Configuration](bytes.NewBufferString(body))
	if err != nil || evolution.Digest([]byte(body)) != result.Digest || config.GroupID != group || config.Revision != revision || config.Validate() != nil {
		return result, ErrInvalid
	}
	result.Configuration = config
	return result, nil
}
