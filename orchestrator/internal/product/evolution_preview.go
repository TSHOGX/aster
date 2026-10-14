package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type EvolutionPreviewInput struct {
	ExpectedRevision      int64 `json:"expected_revision"`
	ExpectedDraftRevision int64 `json:"expected_draft_revision"`
	AutoEnabled           bool  `json:"auto_enabled"`
}
type EvolutionPreview struct {
	DraftRevision         int64    `json:"draft_revision"`
	ConfigurationRevision int64    `json:"configuration_revision"`
	Digest                string   `json:"digest,omitempty"`
	CentralValid          bool     `json:"central_valid"`
	Issues                []string `json:"issues"`
	RequiredParticipants  []string `json:"required_participants"`
}

// PreviewEvolutionDraft performs central checks only. It creates no reservations
// or operation; participants must still validate the exact configuration.
func (s *Store) PreviewEvolutionDraft(ctx context.Context, token, group string, input EvolutionPreviewInput) (EvolutionPreview, error) {
	result := EvolutionPreview{Issues: []string{}, RequiredParticipants: []string{"studio", "evaluation-space"}}
	if !validID(group) || input.ExpectedRevision < 0 || input.ExpectedDraftRevision < 1 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT revision,body FROM evolution_configuration_drafts WHERE group_id=? ORDER BY revision DESC LIMIT 1", group).Scan(&result.DraftRevision, &raw); err != nil {
			return err
		}
		if result.DraftRevision != input.ExpectedDraftRevision {
			return ErrConflict
		}
		var current int64
		var state string
		err := tx.QueryRowContext(ctx, "SELECT desired_revision,state FROM evolution_group_configs WHERE group_id=?", group).Scan(&current, &state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != input.ExpectedRevision {
			return ErrConflict
		}
		if state == "activating" || state == "failed" || state == "discarding" {
			result.Issues = append(result.Issues, "activation_pending")
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewBufferString(raw))
		if err != nil {
			return ErrInvalid
		}
		config.Revision = current + 1
		config.AutoEnabled = input.AutoEnabled
		result.ConfigurationRevision = config.Revision
		if config.Validate() != nil {
			result.Issues = append(result.Issues, "configuration_incomplete")
		} else {
			if err = authorizeEvolutionSources(ctx, tx, config); err != nil {
				if !errors.Is(err, ErrForbidden) {
					return err
				}
				result.Issues = append(result.Issues, "source_authority_required")
			}
			body, err := json.Marshal(config)
			if err != nil {
				return err
			}
			result.Digest = evolution.Digest(body)
		}
		result.CentralValid = len(result.Issues) == 0
		return nil
	})
	return result, err
}
