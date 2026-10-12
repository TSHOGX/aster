package product

import (
	"context"
	"encoding/json"
	"errors"
)

type EffectiveAccess struct {
	AccountStatus  string                            `json:"account_status"`
	ManagementRole string                            `json:"management_role"`
	AccountID      string                            `json:"account_id"`
	Revision       int64                             `json:"revision"`
	Actions        map[ProductAction]EffectiveAction `json:"actions"`
}

// Account status fences product authority. Password setup/session state belongs
// to interactive authentication and must not stop autonomous work after a reset.
func activeAccount(ctx context.Context, q queryer, id string) error {
	var status string
	if err := missing(q.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=?`, id).Scan(&status)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrUnauthorized
		}
		return err
	}
	if status != "active" {
		return Admission("account_suspended", ErrForbidden)
	}
	return nil
}
func policyBindings(ctx context.Context, q queryer, account, exclude string) ([]PolicyBinding, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id,g.name,p.id,p.name,p.description,p.revision,p.statements FROM account_memberships m JOIN account_groups g ON g.id=m.group_id JOIN group_policy_bindings b ON b.group_id=g.id JOIN permission_policies p ON p.id=b.policy_id WHERE m.account_id=? AND g.id<>? ORDER BY g.id,p.id`, account, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PolicyBinding{}
	for rows.Next() {
		var binding PolicyBinding
		var raw string
		if err = rows.Scan(&binding.GroupID, &binding.GroupName, &binding.Policy.ID, &binding.Policy.Name, &binding.Policy.Description, &binding.Policy.Revision, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &binding.Policy.Statements); err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}
func effectiveAccess(ctx context.Context, q queryer, id string) (EffectiveAccess, error) {
	result := EffectiveAccess{AccountID: id}
	if err := missing(q.QueryRowContext(ctx, `SELECT authorization_revision,role,status FROM accounts WHERE id=?`, id).Scan(&result.Revision, &result.ManagementRole, &result.AccountStatus)); err != nil {
		return result, err
	}
	bindings, err := policyBindings(ctx, q, id, "")
	if err != nil {
		return result, err
	}
	result.Actions, err = ResolveAccess(bindings)
	if err != nil {
		return result, err
	}
	for action, value := range result.Actions {
		if result.AccountStatus != "active" || action == ActionAdminContentRead && result.ManagementRole != "admin" {
			value.Allowed = false
			result.Actions[action] = value
		}
	}
	return result, nil
}
func (s *Store) Capabilities(ctx context.Context, token string) (EffectiveAccess, error) {
	session, err := s.normalLogin(ctx, s.reader, token)
	if err != nil {
		return EffectiveAccess{}, err
	}
	return effectiveAccess(ctx, s.reader, session.Account.ID)
}
func (s *Store) AdminEffectiveAccess(ctx context.Context, token, id string) (EffectiveAccess, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return EffectiveAccess{}, err
	}
	return effectiveAccess(ctx, s.reader, id)
}

// authorizeAction is the service-level grant check used at admission boundaries.
// Ownership is checked separately first for resource-specific operations.
func authorizeAction(ctx context.Context, q queryer, actor string, action ProductAction) error {
	if !validProductAction(action) {
		return ErrInvalid
	}
	if err := activeAccount(ctx, q, actor); err != nil {
		return err
	}
	if action == ActionAdminContentRead {
		var role string
		if err := q.QueryRowContext(ctx, `SELECT role FROM accounts WHERE id=?`, actor).Scan(&role); err != nil {
			return err
		}
		if role != "admin" {
			return ErrForbidden
		}
	}
	// JSON keys come only from the closed registry, never from a browser header.
	var allowed, denied int
	err := q.QueryRowContext(ctx, `SELECT coalesce(max(json_extract(p.statements,?)='allow'),0),coalesce(max(json_extract(p.statements,?)='deny'),0) FROM account_memberships m JOIN group_policy_bindings b ON b.group_id=m.group_id JOIN permission_policies p ON p.id=b.policy_id WHERE m.account_id=?`, `$."`+string(action)+`"`, `$."`+string(action)+`"`, actor).Scan(&allowed, &denied)
	if err != nil {
		return err
	}
	if allowed == 0 || denied != 0 {
		return Admission("permission_revoked", ErrForbidden)
	}
	return nil
}
