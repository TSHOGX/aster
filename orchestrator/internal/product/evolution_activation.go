package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
)

type EvolutionGroupConfig struct {
	GroupID         string `json:"group_id"`
	DesiredRevision int64  `json:"desired_revision"`
	ActiveRevision  int64  `json:"active_revision"`
	DesiredDigest   string `json:"desired_digest"`
	ActiveDigest    string `json:"active_digest"`
	AutoEnabled     bool   `json:"auto_enabled"`
	State           string `json:"state"`
	OperationID     string `json:"operation_id"`
}
type EvolutionActivationInput struct {
	RequestID        string                  `json:"request_id"`
	ExpectedRevision int64                   `json:"expected_revision"`
	Configuration    evolution.Configuration `json:"configuration"`
}

func (s *Store) EvolutionConfig(ctx context.Context, token, group string) (EvolutionGroupConfig, error) {
	if !validID(group) {
		return EvolutionGroupConfig{}, ErrInvalid
	}
	var c EvolutionGroupConfig
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT group_id,desired_revision,active_revision,desired_digest,active_digest,auto_enabled,state,operation_id FROM evolution_group_configs WHERE group_id=?", group).Scan(&c.GroupID, &c.DesiredRevision, &c.ActiveRevision, &c.DesiredDigest, &c.ActiveDigest, &c.AutoEnabled, &c.State, &c.OperationID)
	})
	return c, err
}
func (s *Store) ActivateEvolutionConfig(ctx context.Context, token, group string, input EvolutionActivationInput) (EvolutionGroupConfig, error) {
	return s.activateEvolutionConfig(ctx, token, group, input, 0)
}

func (s *Store) activateEvolutionConfig(ctx context.Context, token, group string, input EvolutionActivationInput, draftRevision int64) (EvolutionGroupConfig, error) {
	var result EvolutionGroupConfig
	if !validID(group) || input.ExpectedRevision < 0 || input.Configuration.GroupID != group || input.Configuration.Revision != input.ExpectedRevision+1 || input.Configuration.Validate() != nil {
		return result, ErrInvalid
	}
	body, err := json.Marshal(input.Configuration)
	if err != nil || len(body) > evolution.MaxDocumentBytes {
		return result, ErrInvalid
	}
	digest := evolution.Digest(body)
	request, err := mutationRequest(input.RequestID, "evolution.config.activate", group, struct {
		Input         EvolutionActivationInput
		DraftRevision int64
	}{input, draftRevision})
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
		if draftRevision > 0 {
			var currentDraft int64
			if err := tx.QueryRowContext(ctx, "SELECT revision FROM evolution_configuration_drafts WHERE group_id=? ORDER BY revision DESC LIMIT 1", group).Scan(&currentDraft); err != nil {
				return err
			}
			if currentDraft != draftRevision {
				return ErrConflict
			}
		}
		if err := authorizeEvolutionSources(ctx, tx, input.Configuration); err != nil {
			return err
		}
		var current EvolutionGroupConfig
		scanErr := tx.QueryRowContext(ctx, "SELECT group_id,desired_revision,active_revision,desired_digest,active_digest,auto_enabled,state,operation_id FROM evolution_group_configs WHERE group_id=?", group).Scan(&current.GroupID, &current.DesiredRevision, &current.ActiveRevision, &current.DesiredDigest, &current.ActiveDigest, &current.AutoEnabled, &current.State, &current.OperationID)
		if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		if errors.Is(scanErr, sql.ErrNoRows) {
			current.GroupID = group
		}
		if current.DesiredRevision != input.ExpectedRevision {
			return ErrConflict
		}
		if current.State == "activating" || current.State == "failed" {
			return ErrBusy
		}
		result = EvolutionGroupConfig{GroupID: group, DesiredRevision: current.DesiredRevision + 1, ActiveRevision: current.ActiveRevision, DesiredDigest: digest, ActiveDigest: current.ActiveDigest, AutoEnabled: input.Configuration.AutoEnabled, State: "activating", OperationID: randomOperationID()}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_group_configs VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(group_id) DO UPDATE SET desired_revision=excluded.desired_revision,desired_digest=excluded.desired_digest,auto_enabled=excluded.auto_enabled,state='activating',operation_id=excluded.operation_id,updated_at=excluded.updated_at`, group, result.DesiredRevision, result.ActiveRevision, result.DesiredDigest, result.ActiveDigest, result.AutoEnabled, result.State, result.OperationID, s.timestamp()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_configuration_revisions(group_id,revision,operation_id,digest,body,created_at) VALUES(?,?,?,?,?,?)`, group, result.DesiredRevision, result.OperationID, digest, string(body), s.timestamp()); err != nil {
			return err
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.config.activate", "group", group, "", result.DesiredRevision, result)
	})
	return result, err
}
func (s *Store) acceptEvolutionParticipant(ctx context.Context, token string, receipt evolution.ActivationReceipt) error {
	operation, participant, digest := receipt.OperationID, receipt.Participant, receipt.ConfigurationDigest
	if receipt.Generation < 1 || receipt.State != "adopted" || (participant != "studio" && participant != "evaluation-space") {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		var group, state string
		if err = tx.QueryRowContext(ctx, "SELECT group_id,state FROM evolution_group_configs WHERE operation_id=? AND desired_digest=? AND desired_revision=?", operation, digest, receipt.Generation).Scan(&group, &state); err != nil {
			return err
		}
		if state != "activating" && state != "active" {
			return ErrConflict
		}
		allowed, err := authorizeEvolutionProjection(ctx, tx, ProjectionScope{GroupID: group, OperationID: operation, Revision: receipt.Generation, Digest: digest})
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var existing int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND participant=? AND config_digest=? AND state='accepted'", operation, participant, digest).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		if state == "active" {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_activation_receipts VALUES(?,?,?,'accepted','',?) ON CONFLICT(operation_id,participant) DO UPDATE SET state='accepted',error=''`, operation, participant, digest, s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.activation.receipt", "group", group, operation, 0, map[string]string{"participant": participant})
	})
}
func (s *Store) SettleEvolutionActivation(ctx context.Context, token, operation string) (EvolutionGroupConfig, error) {
	var result EvolutionGroupConfig
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, "SELECT group_id,desired_revision,active_revision,desired_digest,active_digest,auto_enabled,state,operation_id FROM evolution_group_configs WHERE operation_id=?", operation).Scan(&result.GroupID, &result.DesiredRevision, &result.ActiveRevision, &result.DesiredDigest, &result.ActiveDigest, &result.AutoEnabled, &result.State, &result.OperationID); err != nil {
			return err
		}
		if result.State == "active" {
			return nil
		}
		if result.State != "activating" {
			return ErrConflict
		}
		allowed, err := authorizeEvolutionProjection(ctx, tx, ProjectionScope{GroupID: result.GroupID, OperationID: operation, Revision: result.DesiredRevision, Digest: result.DesiredDigest})
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND config_digest=? AND state='accepted' AND participant IN ('studio','evaluation-space')", operation, result.DesiredDigest).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			return ErrBusy
		}
		result.ActiveRevision = result.DesiredRevision
		result.ActiveDigest = result.DesiredDigest
		result.State = "active"
		if _, err = tx.ExecContext(ctx, "UPDATE evolution_group_configs SET active_revision=?,active_digest=?,state='active' WHERE operation_id=?", result.ActiveRevision, result.ActiveDigest, operation); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.config.activate", "group", result.GroupID, operation, result.ActiveRevision, result)
	})
	return result, err
}
func randomOperationID() string { return "evo-" + uuid.NewString() }
