package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Source policies are central authorization records. Configuration authors must
// pin these records; a configuration's source flags never create a grant.
type EvolutionSourcePolicyInput struct {
	RequestID        string                  `json:"request_id"`
	ExpectedRevision int64                   `json:"expected_revision"`
	Source           evolution.SourceBinding `json:"source"`
}

func (s *Store) SetEvolutionSourcePolicy(ctx context.Context, token, group string, input EvolutionSourcePolicyInput) (evolution.SourceBinding, error) {
	var result evolution.SourceBinding
	source := input.Source
	check := evolution.Configuration{GroupID: group, Revision: 1, Sources: []evolution.SourceBinding{source}, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	if input.ExpectedRevision < 0 || source.PolicyRevision != input.ExpectedRevision+1 || source.CaptureFrontier != "" || check.Validate() != nil {
		return result, ErrInvalid
	}
	raw, err := json.Marshal(source)
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.source.policy", group+"/"+source.ID, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		receipt, replayed, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replayed {
			return json.Unmarshal(receipt, &result)
		}
		var revision int64
		err = tx.QueryRowContext(ctx, "SELECT revision FROM evolution_source_policies WHERE group_id=? AND source_id=?", group, source.ID).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision != input.ExpectedRevision {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_source_policies(group_id,source_id,revision,body) VALUES(?,?,?,?) ON CONFLICT(group_id,source_id) DO UPDATE SET revision=excluded.revision,body=excluded.body`, group, source.ID, source.PolicyRevision, string(raw)); err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		result = source
		return s.auditEvent(ctx, tx, actor.ID, "evolution.source.policy", "group", group, input.RequestID, source.PolicyRevision, source)
	})
	return result, err
}

func authorizeEvolutionSources(ctx context.Context, q queryer, config evolution.Configuration) error {
	for _, source := range config.Sources {
		var raw string
		if err := q.QueryRowContext(ctx, "SELECT body FROM evolution_source_policies WHERE group_id=? AND source_id=? AND revision=?", config.GroupID, source.ID, source.PolicyRevision).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
		expected, err := json.Marshal(source)
		if err != nil || string(expected) != raw {
			return ErrForbidden
		}
	}
	return nil
}
