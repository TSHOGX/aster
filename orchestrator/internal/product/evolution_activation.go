package product

import (
	"context"
	"database/sql"
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
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Digest           string `json:"digest"`
	AutoEnabled      bool   `json:"auto_enabled"`
}
type EvolutionActivationReceipt struct {
	Participant  string `json:"participant"`
	ConfigDigest string `json:"config_digest"`
	State        string `json:"state"`
	Error        string `json:"error"`
}

func (s *Store) EvolutionConfig(ctx context.Context, token, group string) (EvolutionGroupConfig, error) {
	if !validID(group) {
		return EvolutionGroupConfig{}, ErrInvalid
	}
	var c EvolutionGroupConfig
	err := s.reader.QueryRowContext(ctx, "SELECT group_id,desired_revision,active_revision,desired_digest,active_digest,auto_enabled,state,operation_id FROM evolution_group_configs WHERE group_id=?", group).Scan(&c.GroupID, &c.DesiredRevision, &c.ActiveRevision, &c.DesiredDigest, &c.ActiveDigest, &c.AutoEnabled, &c.State, &c.OperationID)
	return c, err
}
func (s *Store) SaveEvolutionConfig(ctx context.Context, token, group string, input EvolutionActivationInput) (EvolutionGroupConfig, error) {
	var result EvolutionGroupConfig
	if !validID(group) || input.ExpectedRevision < 0 || !validDigestLike(input.Digest) {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
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
		result = EvolutionGroupConfig{GroupID: group, DesiredRevision: current.DesiredRevision + 1, ActiveRevision: current.ActiveRevision, DesiredDigest: input.Digest, ActiveDigest: current.ActiveDigest, AutoEnabled: input.AutoEnabled, State: "activating", OperationID: randomOperationID()}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_group_configs VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(group_id) DO UPDATE SET desired_revision=excluded.desired_revision,desired_digest=excluded.desired_digest,auto_enabled=excluded.auto_enabled,state='activating',operation_id=excluded.operation_id,updated_at=excluded.updated_at`, group, result.DesiredRevision, result.ActiveRevision, result.DesiredDigest, result.ActiveDigest, result.AutoEnabled, result.State, result.OperationID, s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.config.save", "group", group, "", result.DesiredRevision, result)
	})
	return result, err
}
func (s *Store) AcceptEvolutionParticipant(ctx context.Context, token, operation, participant, digest string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		var group string
		if err = tx.QueryRowContext(ctx, "SELECT group_id FROM evolution_group_configs WHERE operation_id=? AND desired_digest=?", operation, digest).Scan(&group); err != nil {
			return err
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
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM evolution_activation_receipts WHERE operation_id=? AND config_digest=? AND state='accepted'", operation, result.DesiredDigest).Scan(&count); err != nil {
			return err
		}
		if count < 2 {
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
func validDigestLike(value string) bool { return len(value) > 0 && len(value) <= 256 }
func randomOperationID() string         { return "evo-" + uuid.NewString() }
