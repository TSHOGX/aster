package product

import (
	"context"
	"database/sql"
)

type EvolutionParticipantStatus struct {
	Participant string `json:"participant"`
	Digest      string `json:"configuration_digest"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
}
type EvolutionOperationStatus struct {
	Current      bool                         `json:"current"`
	OperationID  string                       `json:"operation_id"`
	Revision     int64                        `json:"revision"`
	Digest       string                       `json:"configuration_digest"`
	State        string                       `json:"state"`
	Participants []EvolutionParticipantStatus `json:"participants"`
}

func (s *Store) EvolutionOperation(ctx context.Context, token, group, operation string) (EvolutionOperationStatus, error) {
	result := EvolutionOperationStatus{Participants: []EvolutionParticipantStatus{}}
	if !validID(group) || !validID(operation) {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT o.operation_id,o.revision,o.digest,o.state,EXISTS(SELECT 1 FROM evolution_group_configs c WHERE c.group_id=o.group_id AND c.operation_id=o.operation_id) FROM evolution_operation_states o WHERE o.group_id=? AND o.operation_id=?", group, operation).Scan(&result.OperationID, &result.Revision, &result.Digest, &result.State, &result.Current); err != nil {
			return missing(err)
		}
		rows, err := tx.QueryContext(ctx, "SELECT participant,config_digest,state,error FROM evolution_activation_receipts WHERE operation_id=? ORDER BY participant", operation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value EvolutionParticipantStatus
			if err = rows.Scan(&value.Participant, &value.Digest, &value.State, &value.Error); err != nil {
				return err
			}
			result.Participants = append(result.Participants, value)
		}
		return rows.Err()
	})
	return result, err
}
