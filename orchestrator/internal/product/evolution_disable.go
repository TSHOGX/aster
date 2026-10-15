package product

import (
	"context"
	"database/sql"
	"encoding/json"
)

type DisableEvolutionInput struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// DisableEvolution immediately closes automatic admission in central state. It
// does not suspend the group or invalidate the active manual-run configuration.
func (s *Store) DisableEvolution(ctx context.Context, token, group string, input DisableEvolutionInput) (EvolutionGroupConfig, error) {
	var result EvolutionGroupConfig
	if !validID(group) || input.ExpectedRevision < 1 {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.disable", group, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.manage")
		if err != nil {
			return err
		}
		raw, replayed, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replayed {
			return json.Unmarshal(raw, &result)
		}
		err = tx.QueryRowContext(ctx, "SELECT group_id,desired_revision,active_revision,desired_digest,active_digest,auto_enabled,state,operation_id FROM evolution_group_configs WHERE group_id=?", group).Scan(&result.GroupID, &result.DesiredRevision, &result.ActiveRevision, &result.DesiredDigest, &result.ActiveDigest, &result.AutoEnabled, &result.State, &result.OperationID)
		if err != nil {
			return err
		}
		if result.DesiredRevision != input.ExpectedRevision {
			return ErrConflict
		}
		result.AutoEnabled = false
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_group_configs SET auto_enabled=0,updated_at=? WHERE group_id=?", s.timestamp(), group); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM corpus_collectors WHERE group_id=?", group); err != nil {
			return err
		}
		if err = clearEvolutionTriggers(ctx, tx, group); err != nil {
			return err
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.disable", "group", group, input.RequestID, result.DesiredRevision, map[string]string{"admitted_work": "drain"})
	})
	return result, err
}
