package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type EvolutionGroupGrantInput struct {
	RequestID        string `json:"request_id"`
	AccountID        string `json:"account_id"`
	Action           string `json:"action"`
	Effect           string `json:"effect"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *Store) SetEvolutionGroupGrant(ctx context.Context, token, group string, input EvolutionGroupGrantInput) (int64, error) {
	var result int64
	if !validID(group) || !validID(input.AccountID) || (input.Action != "config.read" && input.Action != "config.manage" && input.Action != "run.start") || (input.Effect != "allow" && input.Effect != "deny") || input.ExpectedRevision < 0 {
		return 0, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.group.grant", group, input)
	if err != nil {
		return 0, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			return json.Unmarshal(raw, &result)
		}
		var revision int64
		err = tx.QueryRowContext(ctx, "SELECT revision FROM evolution_group_grants WHERE group_id=? AND account_id=? AND action=?", group, input.AccountID, input.Action).Scan(&revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if revision != input.ExpectedRevision {
			return ErrConflict
		}
		result = revision + 1
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_group_grants(group_id,account_id,action,effect,revision) VALUES(?,?,?,?,?) ON CONFLICT(group_id,account_id,action) DO UPDATE SET effect=excluded.effect,revision=excluded.revision`, group, input.AccountID, input.Action, input.Effect, result); err != nil {
			return err
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.group.grant", "group", group, input.RequestID, result, input)
	})
	return result, err
}

func (s *Store) evolutionGroupSession(ctx context.Context, q queryer, token, group, action string) (Account, error) {
	session, err := s.normalLogin(ctx, q, token)
	if err != nil {
		return Account{}, err
	}
	if err := authorizeEvolutionGroup(ctx, q, session.Account, group, action); err != nil {
		return Account{}, err
	}
	return session.Account, nil
}
func authorizeEvolutionGroup(ctx context.Context, q queryer, account Account, group, action string) error {
	if err := activeAccount(ctx, q, account.ID); err != nil {
		return err
	}
	var effect string
	err := q.QueryRowContext(ctx, "SELECT effect FROM evolution_group_grants WHERE group_id=? AND account_id=? AND action=?", group, account.ID, action).Scan(&effect)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if effect == "deny" {
		return ErrForbidden
	}
	if account.Role == "admin" {
		return nil
	}
	var member int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM account_memberships WHERE group_id=? AND account_id=?", group, account.ID).Scan(&member); err != nil {
		return err
	}
	if member != 1 || effect != "allow" {
		return ErrForbidden
	}
	return nil
}
