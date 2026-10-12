package product

import (
	"context"
	"database/sql"
)

type GroupResourcePolicy struct {
	GroupID    string                                    `json:"group_id"`
	Revision   int64                                     `json:"revision"`
	Dimensions map[ResourceDimension]ResourceEntitlement `json:"dimensions"`
}
type UserResourceSettings struct {
	AccountID             string                                  `json:"account_id"`
	Revision              int64                                   `json:"revision"`
	AuthorizationRevision int64                                   `json:"authorization_revision"`
	Overrides             ResourceOverrides                       `json:"overrides"`
	Effective             map[ResourceDimension]EffectiveResource `json:"effective"`
}

func resourceOverrides(ctx context.Context, q queryer, account string) (map[ResourceDimension]*int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT dimension,requested FROM account_resource_overrides WHERE account_id=? ORDER BY dimension`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ResourceDimension]*int64{}
	for rows.Next() {
		var dimension ResourceDimension
		var requested int64
		if err = rows.Scan(&dimension, &requested); err != nil {
			return nil, err
		}
		out[dimension] = &requested
	}
	return out, rows.Err()
}
func userResourceSettings(ctx context.Context, q queryer, account string) (UserResourceSettings, error) {
	out := UserResourceSettings{AccountID: account}
	if err := missing(q.QueryRowContext(ctx, `SELECT authorization_revision,coalesce((SELECT revision FROM account_resource_settings WHERE account_id=accounts.id),0) FROM accounts WHERE id=?`, account).Scan(&out.AuthorizationRevision, &out.Revision)); err != nil {
		return out, err
	}
	var err error
	out.Overrides, err = resourceOverrides(ctx, q, account)
	if err != nil {
		return out, err
	}
	groups, err := groupResources(ctx, q, account, "")
	if err != nil {
		return out, err
	}
	platform, err := platformResourceLimits(ctx, q)
	if err != nil {
		return out, err
	}
	out.Effective, err = ResolveResources(groups, platform, out.Overrides)
	return out, err
}
func groupResourcePolicy(ctx context.Context, q queryer, id string) (GroupResourcePolicy, error) {
	out := GroupResourcePolicy{GroupID: id, Dimensions: map[ResourceDimension]ResourceEntitlement{}}
	if err := missing(q.QueryRowContext(ctx, `SELECT revision FROM account_groups WHERE id=?`, id).Scan(&out.Revision)); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `SELECT dimension,default_value,ceiling FROM group_resource_policy WHERE group_id=? ORDER BY dimension`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var dimension ResourceDimension
		var value ResourceEntitlement
		if err = rows.Scan(&dimension, &value.Default, &value.Ceiling); err != nil {
			return out, err
		}
		out.Dimensions[dimension] = value
	}
	return out, rows.Err()
}
func (s *Store) GroupResourcePolicy(ctx context.Context, token, id string) (GroupResourcePolicy, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GroupResourcePolicy{}, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return GroupResourcePolicy{}, err
	}
	out, err := groupResourcePolicy(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) UserResourceSettings(ctx context.Context, token, id string) (UserResourceSettings, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UserResourceSettings{}, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return UserResourceSettings{}, err
	}
	out, err := userResourceSettings(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
