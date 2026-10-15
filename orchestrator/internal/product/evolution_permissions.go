package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"errors"
)

type EvolutionSourceStatus struct {
	SourceID string `json:"source_id"`
	Valid    bool   `json:"valid"`
}
type EvolutionPermissions struct {
	Sources               []EvolutionSourceStatus `json:"sources"`
	AccountID             string                  `json:"account_id"`
	CanManage             bool                    `json:"can_manage"`
	CanRun                bool                    `json:"can_run"`
	CanReadEvidence       bool                    `json:"can_read_evidence"`
	CanBackfill           bool                    `json:"can_backfill"`
	CanManageGrants       bool                    `json:"can_manage_grants"`
	EvidenceGrantRevision int64                   `json:"evidence_grant_revision"`
}

// Permissions are a current UI projection, never an authorization cache.
func (s *Store) EvolutionPermissions(ctx context.Context, token, group string) (EvolutionPermissions, error) {
	result := EvolutionPermissions{Sources: []EvolutionSourceStatus{}}
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read")
		if err != nil {
			return err
		}
		var found string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM account_groups WHERE id=?", group).Scan(&found); err != nil {
			return missing(err)
		}
		result.AccountID = actor.ID
		result.CanManageGrants = actor.Role == "admin"
		check := func(err error) (bool, error) {
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrUnauthorized) {
				return false, nil
			}
			return err == nil, err
		}
		if result.CanManage, err = check(authorizeEvolutionGroup(ctx, tx, actor, group, "config.manage")); err != nil {
			return err
		}
		if result.CanRun, err = check(authorizeEvolutionGroup(ctx, tx, actor, group, "run.start")); err != nil {
			return err
		}
		if result.CanReadEvidence, err = check(authorizeCorpusEvidence(ctx, tx, actor, group)); err != nil {
			return err
		}
		result.CanBackfill = result.CanManageGrants && result.CanManage && result.CanRun && result.CanReadEvidence
		var operation string
		err = tx.QueryRowContext(ctx, "SELECT r.operation_id FROM evolution_group_configs c JOIN evolution_configuration_revisions r ON r.group_id=c.group_id AND r.revision=c.active_revision WHERE c.group_id=?", group).Scan(&operation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			projection, err := readEvolutionProjection(ctx, tx, operation)
			if err != nil {
				return err
			}
			for _, source := range projection.Configuration.Sources {
				valid, err := check(authorizeEvolutionSources(ctx, tx, evolution.Configuration{GroupID: group, Sources: []evolution.SourceBinding{source}}))
				if err != nil {
					return err
				}
				result.Sources = append(result.Sources, EvolutionSourceStatus{SourceID: source.ID, Valid: valid})
			}
		}
		err = tx.QueryRowContext(ctx, "SELECT revision FROM evolution_group_grants WHERE group_id=? AND account_id=? AND action='source.read'", group, actor.ID).Scan(&result.EvidenceGrantRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return result, err
}
