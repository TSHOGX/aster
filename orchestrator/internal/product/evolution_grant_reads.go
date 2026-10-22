package product

import (
	"context"
	"database/sql"
	"errors"
)

var evolutionGroupActions = []string{"config.read", "config.manage", "run.start", "source.read", "workflow.edit"}

type EvolutionGrantState struct {
	Action   string `json:"action"`
	Effect   string `json:"effect"`
	Revision int64  `json:"revision"`
	Allowed  bool   `json:"allowed"`
}
type EvolutionAccountGrants struct {
	AccountID   string                `json:"account_id"`
	DisplayName string                `json:"display_name"`
	Status      string                `json:"status"`
	Role        string                `json:"role"`
	Member      bool                  `json:"member"`
	Grants      []EvolutionGrantState `json:"grants"`
}

// Grant management reads require platform administration, independently of a
// target group's configuration/evidence grants (including an explicit denial).
func (s *Store) EvolutionAccountGrants(ctx context.Context, token, group, account string) (EvolutionAccountGrants, error) {
	result := EvolutionAccountGrants{Grants: []EvolutionGrantState{}}
	if !validID(group) || !validID(account) {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.adminSession(ctx, tx, token); err != nil {
			return err
		}
		var found string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM account_groups WHERE id=?", group).Scan(&found); err != nil {
			return missing(err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT id,display_name,status,role,EXISTS(SELECT 1 FROM account_memberships WHERE group_id=? AND account_id=accounts.id) FROM accounts WHERE id=?`, group, account).Scan(&result.AccountID, &result.DisplayName, &result.Status, &result.Role, &result.Member); err != nil {
			return missing(err)
		}
		actor := Account{ID: account, Role: result.Role}
		for _, action := range evolutionGroupActions {
			grant := EvolutionGrantState{Action: action, Effect: "unset"}
			err := tx.QueryRowContext(ctx, "SELECT effect,revision FROM evolution_group_grants WHERE group_id=? AND account_id=? AND action=?", group, account, action).Scan(&grant.Effect, &grant.Revision)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if action == "source.read" {
				err = authorizeCorpusEvidence(ctx, tx, actor, group)
			} else {
				err = authorizeEvolutionGroup(ctx, tx, actor, group, action)
			}
			if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrUnauthorized) {
				return err
			}
			grant.Allowed = err == nil
			result.Grants = append(result.Grants, grant)
		}
		return nil
	})
	return result, err
}
