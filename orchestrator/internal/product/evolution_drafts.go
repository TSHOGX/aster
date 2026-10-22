package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Draft revisions are editing concurrency tokens, independent of activation revisions.
type EvolutionDraft struct {
	Settings      *evolution.DraftSettings `json:"settings,omitempty"`
	Revision      int64                    `json:"revision"`
	Configuration evolution.Configuration  `json:"configuration"`
}
type EvolutionDraftInput struct {
	Settings *evolution.DraftSettings `json:"settings,omitempty"`
	// TemplatePolicies is populated only by the authenticated catalog gateway.
	TemplatePolicies map[evolution.Revision]evolution.EvaluationPolicy `json:"-"`
	RequestID        string                                            `json:"request_id"`
	ExpectedRevision int64                                             `json:"expected_revision"`
	Configuration    evolution.Configuration                           `json:"configuration"`
}

// EvolutionDraftReplay checks the original request before a catalog read. A
// lost save reply remains recoverable even while the catalog is unavailable.
func (s *Store) EvolutionDraftReplay(ctx context.Context, token, group string, input EvolutionDraftInput) (EvolutionDraft, bool, error) {
	var result EvolutionDraft
	var found bool
	request, err := mutationRequest(input.RequestID, "evolution.draft.save", group, input)
	if err != nil {
		return result, false, err
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
		found = replayed
		if found {
			return json.Unmarshal(raw, &result)
		}
		return nil
	})
	return result, found, err
}

func (s *Store) SaveEvolutionDraft(ctx context.Context, token, group string, input EvolutionDraftInput) (EvolutionDraft, error) {
	var result EvolutionDraft
	// A draft is never an enabled admission configuration. Its activation identity
	// is assigned only when it is promoted to a desired revision.
	if !validID(group) || input.ExpectedRevision < 0 || input.Configuration.GroupID != group || input.Configuration.Revision != 0 || input.Configuration.AutoEnabled {
		return result, ErrInvalid
	}
	settings, err := json.Marshal(input.Settings)
	if err != nil {
		return result, ErrInvalid
	}
	body, err := json.Marshal(input.Configuration)
	if err != nil || len(body)+len(settings) > evolution.MaxDocumentBytes {
		return result, ErrInvalid
	}
	if _, err = evolution.Decode[evolution.Configuration](bytes.NewReader(body)); err != nil {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.draft.save", group, input)
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
		resolved, err := evolution.ResolveDraftSettings(input.Configuration, input.Settings, input.TemplatePolicies)
		if err != nil {
			return ErrInvalid
		}
		body, err := json.Marshal(resolved)
		if err != nil || len(body)+len(settings) > evolution.MaxDocumentBytes {
			return ErrInvalid
		}
		var revision int64
		err = tx.QueryRowContext(ctx, "SELECT revision FROM evolution_configuration_drafts WHERE group_id=? ORDER BY revision DESC LIMIT 1", group).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision != input.ExpectedRevision {
			return ErrConflict
		}
		result = EvolutionDraft{Revision: revision + 1, Configuration: resolved, Settings: input.Settings}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_configuration_drafts(group_id,revision,body,settings,updated_at) VALUES(?,?,?,?,?)`, group, result.Revision, string(body), string(settings), s.timestamp()); err != nil {
			return err
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.draft.save", "group", group, input.RequestID, result.Revision, nil)
	})
	return result, err
}

func (s *Store) EvolutionDraft(ctx context.Context, token, group string) (EvolutionDraft, error) {
	return s.evolutionDraftRevision(ctx, token, group, 0)
}
func (s *Store) evolutionDraftRevision(ctx context.Context, token, group string, revision int64) (EvolutionDraft, error) {
	var result EvolutionDraft
	if !validID(group) {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		var body, settings string
		if err := tx.QueryRowContext(ctx, "SELECT revision,body,settings FROM evolution_configuration_drafts WHERE group_id=? AND (?=0 OR revision=?) ORDER BY revision DESC LIMIT 1", group, revision, revision).Scan(&result.Revision, &body, &settings); err != nil {
			return missing(err)
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewBufferString(body))
		if err != nil || config.GroupID != group || config.Revision != 0 || config.AutoEnabled {
			return ErrInvalid
		}
		if err = json.Unmarshal([]byte(settings), &result.Settings); err != nil {
			return ErrInvalid
		}
		result.Configuration = config
		return nil
	})
	return result, err
}

// ActivateEvolutionDraft freezes a complete draft into a new desired projection.
// The draft CAS is checked again inside the activation transaction.
func (s *Store) ActivateEvolutionDraft(ctx context.Context, token, group string, input EvolutionDraftActivationInput) (EvolutionGroupConfig, error) {
	if input.ExpectedDraftRevision < 1 || input.ExpectedRevision < 0 {
		return EvolutionGroupConfig{}, ErrInvalid
	}
	draft, err := s.evolutionDraftRevision(ctx, token, group, input.ExpectedDraftRevision)
	if err != nil {
		return EvolutionGroupConfig{}, err
	}
	if draft.Revision != input.ExpectedDraftRevision {
		return EvolutionGroupConfig{}, ErrConflict
	}
	config := draft.Configuration
	config.Revision = input.ExpectedRevision + 1
	config.AutoEnabled = input.AutoEnabled
	return s.activateEvolutionConfig(ctx, token, group, EvolutionActivationInput{RequestID: input.RequestID, ExpectedRevision: input.ExpectedRevision, Configuration: config}, input.ExpectedDraftRevision)
}

type EvolutionDraftActivationInput struct {
	RequestID             string `json:"request_id"`
	ExpectedRevision      int64  `json:"expected_revision"`
	ExpectedDraftRevision int64  `json:"expected_draft_revision"`
	AutoEnabled           bool   `json:"auto_enabled"`
}
