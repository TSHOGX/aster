package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

type EvolutionPreviewInput struct {
	ExpectedRevision      int64 `json:"expected_revision"`
	ExpectedDraftRevision int64 `json:"expected_draft_revision"`
	AutoEnabled           bool  `json:"auto_enabled"`
}
type EvolutionBindingChange struct {
	BindingID string             `json:"binding_id"`
	Kind      string             `json:"kind"`
	Before    *evolution.Binding `json:"before,omitempty"`
	After     *evolution.Binding `json:"after,omitempty"`
}
type EvolutionPreview struct {
	CatalogValid          *bool                    `json:"catalog_valid,omitempty"`
	CatalogError          string                   `json:"catalog_error,omitempty"`
	ActiveRevision        int64                    `json:"active_revision"`
	AdmittedRuns          int                      `json:"admitted_runs"`
	Changes               []EvolutionBindingChange `json:"changes"`
	Configuration         *evolution.Configuration `json:"configuration,omitempty"`
	ValidationError       string                   `json:"validation_error,omitempty"`
	DraftRevision         int64                    `json:"draft_revision"`
	ConfigurationRevision int64                    `json:"configuration_revision"`
	Digest                string                   `json:"digest,omitempty"`
	CentralValid          bool                     `json:"central_valid"`
	Issues                []string                 `json:"issues"`
	RequiredParticipants  []string                 `json:"required_participants"`
}

// PreviewEvolutionDraft performs central checks only. It creates no reservations
// or operation; participants must still validate the exact configuration.
func (s *Store) PreviewEvolutionDraft(ctx context.Context, token, group string, input EvolutionPreviewInput) (EvolutionPreview, error) {
	result := EvolutionPreview{Changes: []EvolutionBindingChange{}, Issues: []string{}, RequiredParticipants: []string{"studio", "evolution-space"}}
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
		err := tx.QueryRowContext(ctx, "SELECT desired_revision,state,active_revision FROM evolution_group_configs WHERE group_id=?", group).Scan(&current, &state, &result.ActiveRevision)
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
		if validationErr := config.Validate(); validationErr != nil {
			result.ValidationError = validationErr.Error()
			result.Issues = append(result.Issues, "configuration_incomplete")
		} else {
			for _, binding := range config.Bindings {
				if binding.Trigger == "schedule" {
					schedule, _, scheduleErr := parseRecurrence(binding.Schedule, binding.Timezone)
					if scheduleErr != nil || schedule.Next(s.now()).IsZero() {
						result.Issues = append(result.Issues, "invalid_schedule")
						result.ValidationError = "A scheduled binding requires a valid future recurrence."
						break
					}
				}
			}
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
		result.Configuration = &config
		previous := map[string]evolution.Binding{}
		if result.ActiveRevision > 0 {
			var body []byte
			if err = tx.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", group, result.ActiveRevision).Scan(&body); err != nil {
				return err
			}
			active, decodeErr := evolution.Decode[evolution.Configuration](bytes.NewReader(body))
			if decodeErr != nil {
				return decodeErr
			}
			for _, binding := range active.Bindings {
				previous[binding.ID] = binding
			}
		}
		for _, binding := range config.Bindings {
			old, ok := previous[binding.ID]
			if !ok {
				result.Changes = append(result.Changes, EvolutionBindingChange{BindingID: binding.ID, Kind: "added", After: &binding})
			} else {
				before, _ := json.Marshal(old)
				after, _ := json.Marshal(binding)
				if !bytes.Equal(before, after) {
					result.Changes = append(result.Changes, EvolutionBindingChange{BindingID: binding.ID, Kind: "changed", Before: &old, After: &binding})
				}
			}
			delete(previous, binding.ID)
		}
		for _, binding := range previous {
			result.Changes = append(result.Changes, EvolutionBindingChange{BindingID: binding.ID, Kind: "removed", Before: &binding})
		}
		sort.Slice(result.Changes, func(i, j int) bool { return result.Changes[i].BindingID < result.Changes[j].BindingID })
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM generation_authorizations WHERE group_id=? AND state IN('admitted','running','paused','cancelling')", group).Scan(&result.AdmittedRuns); err != nil {
			return err
		}
		result.CentralValid = len(result.Issues) == 0
		return nil
	})
	return result, err
}
