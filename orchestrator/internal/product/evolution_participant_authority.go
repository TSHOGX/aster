package product

import (
	"context"
	"database/sql"
)

// EvolutionParticipantActor resolves the saved operation's initiator, never a
// caller-selected subject. Sessions may expire; current account/grants still apply.
func (s *Store) EvolutionParticipantActor(ctx context.Context, scope ProjectionScope, action string) (string, error) {
	var subject string
	if action != "reserve" && action != "adopt" && action != "lookup" && action != "cancel" {
		return "", ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if action == "cancel" || action == "lookup" {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_group_configs WHERE group_id=? AND operation_id=? AND desired_revision=? AND desired_digest=? AND state='failed'", scope.GroupID, scope.OperationID, scope.Revision, scope.Digest).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				if action == "cancel" {
					return ErrForbidden
				}
				allowed, err := authorizeEvolutionProjection(ctx, tx, scope)
				if err != nil {
					return err
				}
				if !allowed {
					return ErrForbidden
				}
			}
		} else {
			allowed, err := authorizeEvolutionProjection(ctx, tx, scope)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrForbidden
			}
		}
		var account Account
		if err := tx.QueryRowContext(ctx, "SELECT a.id,a.role FROM evolution_configuration_revisions r JOIN accounts a ON a.id=r.actor_id WHERE r.operation_id=? AND r.group_id=? AND r.revision=? AND r.digest=?", scope.OperationID, scope.GroupID, scope.Revision, scope.Digest).Scan(&account.ID, &account.Role); err != nil {
			return err
		}
		if err := authorizeEvolutionGroup(ctx, tx, account, scope.GroupID, "config.manage"); err != nil {
			return err
		}
		subject = account.ID
		return nil
	})
	return subject, err
}
