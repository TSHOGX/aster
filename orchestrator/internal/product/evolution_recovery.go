package product

import (
	"context"
	"database/sql"
)

// FailEvolutionParticipant records an explicit failure without replacing the
// accepted configuration. Failure codes are bounded identifiers, not raw secrets
// or remote error payloads.
func (s *Store) FailEvolutionParticipant(ctx context.Context, token, operation, participant, digest, code string) error {
	if !validID(code) || (participant != "studio" && participant != "evaluation-space") {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		return s.failEvolutionParticipant(ctx, tx, actor.ID, operation, participant, digest, code)
	})
}
func (s *Store) failEvolutionParticipant(ctx context.Context, tx *sql.Tx, actor, operation, participant, digest, code string) error {
	var group, state string
	if err := tx.QueryRowContext(ctx, "SELECT group_id,state FROM evolution_group_configs WHERE operation_id=? AND desired_digest=?", operation, digest).Scan(&group, &state); err != nil {
		return err
	}
	if state != "activating" && state != "failed" {
		return ErrConflict
	}
	var existing int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND participant=? AND state='accepted'", operation, participant).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evolution_activation_receipts VALUES(?,?,?,'failed',?,?) ON CONFLICT(operation_id,participant) DO UPDATE SET state='failed',error=excluded.error`, operation, participant, digest, code, s.timestamp()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE evolution_group_configs SET state='failed' WHERE operation_id=?", operation); err != nil {
		return err
	}
	return s.auditEvent(ctx, tx, actor, "evolution.activation.failed", "group", group, operation, 0, map[string]string{"participant": participant, "code": code})
}
