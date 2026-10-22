package product

import (
	"context"
	"database/sql"
)

type EvolutionRetryInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// RetryEvolutionOperation records the retry intent separately from the mutable
// operation state. Replaying it after another failure cannot dispatch again.
func (s *Store) RetryEvolutionOperation(ctx context.Context, token, group, operation string, input EvolutionRetryInput) error {
	return s.changeFailedEvolutionOperation(ctx, token, group, operation, input, "retry", "activating")
}
func (s *Store) DiscardEvolutionOperation(ctx context.Context, token, group, operation string, input EvolutionRetryInput) error {
	return s.changeFailedEvolutionOperation(ctx, token, group, operation, input, "discard", "discarding")
}
func (s *Store) changeFailedEvolutionOperation(ctx context.Context, token, group, operation string, input EvolutionRetryInput, action, target string) error {
	if !validID(group) || !validID(operation) || input.ExpectedRevision < 1 {
		return ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.activation."+action, group+"/"+operation, input)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.manage")
		if err != nil {
			return err
		}
		_, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			return nil
		}
		var state string
		var revision int64
		if err = tx.QueryRowContext(ctx, "SELECT desired_revision,state FROM evolution_group_configs WHERE group_id=? AND operation_id=?", group, operation).Scan(&revision, &state); err != nil {
			return missing(err)
		}
		if revision != input.ExpectedRevision || state != "failed" {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_group_configs SET state=? WHERE group_id=? AND operation_id=?", target, group, operation); err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, []byte(`{}`)); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.activation."+action, "group", group, operation, revision, nil)
	})
}
