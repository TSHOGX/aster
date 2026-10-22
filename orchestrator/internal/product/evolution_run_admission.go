package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"errors"
)

type EvolutionRunAdmissionScope struct {
	Subject   string `json:"subject"`
	GroupID   string `json:"group_id"`
	BindingID string `json:"binding_id"`
	Revision  int64  `json:"revision"`
	Digest    string `json:"digest"`
	Automatic bool   `json:"automatic"`
}

// AuthorizeEvolutionRunAdmission checks central admission only. Already admitted
// work needs separate effect authority and must not infer it from this decision.
func (s *Store) AuthorizeEvolutionRunAdmission(ctx context.Context, scope EvolutionRunAdmissionScope) (bool, error) {
	if !validID(scope.Subject) || !validID(scope.GroupID) || !validID(scope.BindingID) || scope.Revision < 1 {
		return false, ErrInvalid
	}
	allowed := false
	err := s.write(ctx, func(tx *sql.Tx) error {
		var account Account
		if err := tx.QueryRowContext(ctx, "SELECT id,role FROM accounts WHERE id=?", scope.Subject).Scan(&account.ID, &account.Role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if err := authorizeEvolutionGroup(ctx, tx, account, scope.GroupID, "run.start"); err != nil {
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrUnauthorized) {
				return nil
			}
			return err
		}
		var revision int64
		var digest, state string
		var enabled bool
		err := tx.QueryRowContext(ctx, "SELECT active_revision,active_digest,state,auto_enabled FROM evolution_group_configs WHERE group_id=?", scope.GroupID).Scan(&revision, &digest, &state, &enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if revision != scope.Revision || digest != scope.Digest {
			return nil
		}
		if scope.Automatic && (!enabled || state != "active") {
			return nil
		}
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE group_id=? AND revision=?", scope.GroupID, revision).Scan(&raw); err != nil {
			return err
		}
		config, err := evolution.Decode[evolution.Configuration](bytes.NewReader(raw))
		if err != nil || evolution.Digest(raw) != digest {
			return ErrInvalid
		}
		if err = authorizeEvolutionSources(ctx, tx, config); err != nil {
			if errors.Is(err, ErrForbidden) {
				return nil
			}
			return err
		}
		for _, binding := range config.Bindings {
			if binding.ID == scope.BindingID && binding.Enabled && (!scope.Automatic || binding.Trigger != "manual") {
				allowed = true
				break
			}
		}
		return nil
	})
	return allowed, err
}
