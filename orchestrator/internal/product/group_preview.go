package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

type GroupImpact struct {
	AccountStatus   string                                  `json:"account_status"`
	ManagementRole  string                                  `json:"management_role"`
	AccountID       string                                  `json:"account_id"`
	Username        string                                  `json:"username"`
	BeforeAccess    map[ProductAction]EffectiveAction       `json:"before_access"`
	AfterAccess     map[ProductAction]EffectiveAction       `json:"after_access"`
	BeforeResources map[ResourceDimension]EffectiveResource `json:"before_resources"`
	AfterResources  map[ResourceDimension]EffectiveResource `json:"after_resources"`
}
type GroupDeletionPreview struct {
	Group                Group         `json:"group"`
	ExpectedDependencies string        `json:"expected_dependencies"`
	Users                []GroupImpact `json:"users"`
	NextCursor           string        `json:"next_cursor,omitempty"`
}

func groupResources(ctx context.Context, q queryer, account, exclude string) ([]GroupResources, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id,g.name,g.revision,r.dimension,r.default_value,r.ceiling FROM account_memberships m JOIN account_groups g ON g.id=m.group_id LEFT JOIN group_resource_policy r ON r.group_id=g.id WHERE m.account_id=? AND g.id<>? ORDER BY g.id,r.dimension`, account, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []GroupResources{}
	for rows.Next() {
		var id, name string
		var revision int64
		var dimension sql.NullString
		var initial, ceiling sql.NullInt64
		if err = rows.Scan(&id, &name, &revision, &dimension, &initial, &ceiling); err != nil {
			return nil, err
		}
		if len(result) == 0 || result[len(result)-1].ID != id {
			result = append(result, GroupResources{ID: id, Name: name, Revision: revision, Dimensions: map[ResourceDimension]ResourceEntitlement{}})
		}
		if dimension.Valid {
			result[len(result)-1].Dimensions[ResourceDimension(dimension.String)] = ResourceEntitlement{Default: initial.Int64, Ceiling: ceiling.Int64}
		}
	}
	return result, rows.Err()
}
func platformResourceLimits(ctx context.Context, q queryer) (map[ResourceDimension]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT dimension,maximum FROM platform_resource_limits ORDER BY dimension`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[ResourceDimension]int64{}
	for rows.Next() {
		var dimension ResourceDimension
		var value int64
		if err = rows.Scan(&dimension, &value); err != nil {
			return nil, err
		}
		result[dimension] = value
	}
	return result, rows.Err()
}

// The fingerprint covers only configuration that contributes to this group's
// members. Usage is not a configuration revision and never creates a preview lock.
func groupDeletionDependencies(ctx context.Context, q queryer, id string) (string, error) {
	hash := sha256.New()
	queries := []string{
		`SELECT id,revision FROM account_groups WHERE id=?`,
		`SELECT a.id,a.authorization_revision FROM account_memberships m JOIN accounts a ON a.id=m.account_id WHERE m.group_id=? ORDER BY a.id`,
		`SELECT m.account_id,g.id,g.revision,r.dimension,r.default_value,r.ceiling FROM account_memberships m JOIN account_groups g ON g.id=m.group_id LEFT JOIN group_resource_policy r ON r.group_id=g.id WHERE m.account_id IN (SELECT account_id FROM account_memberships WHERE group_id=?) ORDER BY m.account_id,g.id,r.dimension`,
		`SELECT DISTINCT p.id,p.revision FROM account_memberships m JOIN group_policy_bindings b ON b.group_id=m.group_id JOIN permission_policies p ON p.id=b.policy_id WHERE m.account_id IN (SELECT account_id FROM account_memberships WHERE group_id=?) ORDER BY p.id`,
	}
	for _, query := range queries {
		rows, err := q.QueryContext(ctx, query, id)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				return "", err
			}
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return "", err
			}
			hash.Write(raw)
			hash.Write([]byte{0})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
		hash.Write([]byte{1})
	}
	rows, err := q.QueryContext(ctx, `SELECT dimension,maximum,revision FROM platform_resource_limits ORDER BY dimension`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var dimension string
		var maximum, revision int64
		if err = rows.Scan(&dimension, &maximum, &revision); err != nil {
			return "", err
		}
		raw, _ := json.Marshal([]any{dimension, maximum, revision})
		hash.Write(raw)
		hash.Write([]byte{0})
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
func (s *Store) PreviewGroupDeletion(ctx context.Context, token, id, cursor string, limit int) (GroupDeletionPreview, error) {
	limit, err := adminLimit(limit)
	if err != nil {
		return GroupDeletionPreview{}, err
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GroupDeletionPreview{}, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return GroupDeletionPreview{}, err
	}
	group, err := readGroup(ctx, tx, id)
	if err != nil {
		return GroupDeletionPreview{}, err
	}
	dependencies, err := groupDeletionDependencies(ctx, tx, id)
	if err != nil {
		return GroupDeletionPreview{}, err
	}
	scope := cursorScope("group-delete", []string{id, dependencies})
	after, err := readAdminCursor(cursor, scope)
	if err != nil {
		return GroupDeletionPreview{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.username,a.status,a.role FROM account_memberships m JOIN accounts a ON a.id=m.account_id WHERE m.group_id=? AND a.id>? ORDER BY a.id LIMIT ?`, id, after, limit+1)
	if err != nil {
		return GroupDeletionPreview{}, err
	}
	result := GroupDeletionPreview{Group: group, ExpectedDependencies: dependencies, Users: []GroupImpact{}}
	for rows.Next() {
		var item GroupImpact
		if err = rows.Scan(&item.AccountID, &item.Username, &item.AccountStatus, &item.ManagementRole); err != nil {
			rows.Close()
			return result, err
		}
		result.Users = append(result.Users, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Users) > limit {
		result.Users = result.Users[:limit]
		result.NextCursor = nextAdminCursor(result.Users[limit-1].AccountID, scope)
	}
	platform, err := platformResourceLimits(ctx, tx)
	if err != nil {
		return result, err
	}
	for i := range result.Users {
		item := &result.Users[i]
		before, err := policyBindings(ctx, tx, item.AccountID, "")
		if err != nil {
			return result, err
		}
		after, err := policyBindings(ctx, tx, item.AccountID, id)
		if err != nil {
			return result, err
		}
		item.BeforeAccess, err = ResolveAccess(before)
		if err != nil {
			return result, err
		}
		item.AfterAccess, err = ResolveAccess(after)
		if err != nil {
			return result, err
		}
		for _, access := range []map[ProductAction]EffectiveAction{item.BeforeAccess, item.AfterAccess} {
			for action, value := range access {
				if item.AccountStatus != "active" || action == ActionAdminContentRead && item.ManagementRole != "admin" {
					value.Allowed = false
					access[action] = value
				}
			}
		}
		beforeResources, err := groupResources(ctx, tx, item.AccountID, "")
		if err != nil {
			return result, err
		}
		afterResources, err := groupResources(ctx, tx, item.AccountID, id)
		if err != nil {
			return result, err
		}
		item.BeforeResources, err = ResolveResources(beforeResources, platform, nil)
		if err != nil {
			return result, err
		}
		item.AfterResources, err = ResolveResources(afterResources, platform, nil)
		if err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}
func (s *Store) DeleteGroup(ctx context.Context, token, id string, input GroupDelete) (PolicyResult, error) {
	request, err := mutationRequest(input.RequestID, "group.delete", id, input)
	if err != nil {
		return PolicyResult{}, err
	}
	return s.policyMutation(ctx, token, request, func(tx *sql.Tx, actor Account) (PolicyResult, error) {
		before, err := readGroup(ctx, tx, id)
		if err != nil {
			return PolicyResult{}, err
		}
		if before.Revision != input.ExpectedRevision {
			return PolicyResult{}, ErrConflict
		}
		if before.MemberCount > 0 {
			current, err := groupDeletionDependencies(ctx, tx, id)
			if err != nil {
				return PolicyResult{}, err
			}
			if input.ExpectedDependencies == "" || input.ExpectedDependencies != current {
				return PolicyResult{}, Admission("preview_stale", ErrConflict)
			}
		}
		if err = advanceGroupMembers(ctx, tx, id); err != nil {
			return PolicyResult{}, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE id=?`, id); err != nil {
			return PolicyResult{}, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "group", id, request.ID, before.Revision, map[string]any{"before": before, "affected_users": before.MemberCount})
		return PolicyResult{DeletedID: id}, err
	})
}
