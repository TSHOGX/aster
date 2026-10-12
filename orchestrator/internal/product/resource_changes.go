package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type ResourceChange struct {
	Kind             string                                     `json:"kind"`
	TargetID         string                                     `json:"target_id"`
	ExpectedRevision int64                                      `json:"expected_revision"`
	Dimensions       map[ResourceDimension]*ResourceEntitlement `json:"dimensions,omitempty"`
	Overrides        ResourceOverrides                          `json:"overrides,omitempty"`
}
type ResourceMutation struct {
	ResourceChange
	RequestID            string `json:"request_id"`
	ExpectedDependencies string `json:"expected_dependencies"`
}
type ResourceChangeResult struct {
	Group    *GroupResourcePolicy  `json:"group,omitempty"`
	User     *UserResourceSettings `json:"user,omitempty"`
	Replayed bool                  `json:"replayed"`
}
type ResourceImpact struct {
	Usage            map[ResourceDimension]ResourceUsage     `json:"usage"`
	BeforeOverages   []ResourceFitIssue                      `json:"before_overages"`
	AfterOverages    []ResourceFitIssue                      `json:"after_overages"`
	BeforeSavedPlans SavedPlanConflicts                      `json:"before_saved_plan_conflicts"`
	AfterSavedPlans  SavedPlanConflicts                      `json:"after_saved_plan_conflicts"`
	AccountID        string                                  `json:"account_id"`
	Username         string                                  `json:"username"`
	Before           map[ResourceDimension]EffectiveResource `json:"before"`
	After            map[ResourceDimension]EffectiveResource `json:"after"`
	BeforePlan       []ResourceFitIssue                      `json:"before_plan_issues"`
	AfterPlan        []ResourceFitIssue                      `json:"after_plan_issues"`
}
type ResourceChangePreview struct {
	ExpectedDependencies string           `json:"expected_dependencies"`
	AffectedAccounts     int64            `json:"affected_accounts,string"`
	Users                []ResourceImpact `json:"users"`
	NextCursor           string           `json:"next_cursor,omitempty"`
}

func validateResourceChange(input ResourceChange) error {
	if !validID(input.TargetID) || input.ExpectedRevision < 0 {
		return ErrInvalid
	}
	switch input.Kind {
	case "group":
		if len(input.Dimensions) == 0 || len(input.Overrides) > 0 {
			return ErrInvalid
		}
		for dimension, value := range input.Dimensions {
			if !validResourceDimension(dimension) {
				return Admission("resource_invalid", ErrInvalid)
			}
			if value != nil {
				if err := ValidateGroupResources(map[ResourceDimension]ResourceEntitlement{dimension: *value}); err != nil {
					return err
				}
			}
		}
	case "user":
		if len(input.Overrides) == 0 || len(input.Dimensions) > 0 {
			return ErrInvalid
		}
		for dimension, value := range input.Overrides {
			if !validResourceDimension(dimension) || value != nil && *value < 0 {
				return Admission("resource_invalid", ErrInvalid)
			}
		}
	default:
		return ErrInvalid
	}
	return nil
}
func resourceTargetRevision(ctx context.Context, q queryer, input ResourceChange) error {
	var revision int64
	var err error
	if input.Kind == "group" {
		err = missing(q.QueryRowContext(ctx, `SELECT revision FROM account_groups WHERE id=?`, input.TargetID).Scan(&revision))
	} else {
		err = missing(q.QueryRowContext(ctx, `SELECT coalesce((SELECT revision FROM account_resource_settings WHERE account_id=a.id),0) FROM accounts a WHERE a.id=?`, input.TargetID).Scan(&revision))
	}
	if err != nil {
		return err
	}
	if revision != input.ExpectedRevision {
		return ErrConflict
	}
	return nil
}

const resourceAffectedAccounts = `SELECT a.id FROM accounts a WHERE (?='user' AND a.id=?) OR (?='group' AND EXISTS(SELECT 1 FROM account_memberships m WHERE m.account_id=a.id AND m.group_id=?))`

func resourceDependencies(ctx context.Context, q queryer, input ResourceChange) (string, error) {
	hash := sha256.New()
	configuration, err := runtimeConfigurationDigest(ctx, q)
	if err != nil {
		return "", err
	}
	hash.Write([]byte(configuration))
	// The scope is derived from the target, never from a browser list of users.
	// Usage is excluded: it is observed again at actual allocation, not locked by
	// the preview. Stream rows into a fixed-size digest rather than retaining them.
	affected := resourceAffectedAccounts
	statements := []struct {
		sql  string
		args []any
	}{
		{`SELECT id,revision FROM account_groups WHERE id=?`, []any{input.TargetID}},
		{`SELECT id,authorization_revision,coalesce((SELECT revision FROM account_resource_settings WHERE account_id=accounts.id),0) FROM accounts WHERE id IN (` + affected + `) ORDER BY id`, []any{input.Kind, input.TargetID, input.Kind, input.TargetID}},
		{`SELECT m.account_id,m.group_id,g.revision FROM account_memberships m JOIN account_groups g ON g.id=m.group_id WHERE m.account_id IN (` + affected + `) ORDER BY m.account_id,m.group_id`, []any{input.Kind, input.TargetID, input.Kind, input.TargetID}},
		{`SELECT group_id,dimension,default_value,ceiling FROM group_resource_policy WHERE group_id=? OR group_id IN (SELECT group_id FROM account_memberships WHERE account_id IN (` + affected + `)) ORDER BY group_id,dimension`, []any{input.TargetID, input.Kind, input.TargetID, input.Kind, input.TargetID}},
		{`SELECT account_id,dimension,requested FROM account_resource_overrides WHERE account_id IN (` + affected + `) ORDER BY account_id,dimension`, []any{input.Kind, input.TargetID, input.Kind, input.TargetID}},
		{`SELECT dimension,maximum,revision FROM platform_resource_limits ORDER BY dimension`, nil},
	}
	for _, statement := range statements {
		rows, err := q.QueryContext(ctx, statement.sql, statement.args...)
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
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				break
			}
			raw, e := json.Marshal(values)
			if e != nil {
				err = e
				break
			}
			hash.Write(raw)
			hash.Write([]byte{0})
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return "", err
		}
		hash.Write([]byte{1})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
func proposedResources(ctx context.Context, q queryer, account string, input ResourceChange) (map[ResourceDimension]EffectiveResource, error) {
	groups, err := groupResources(ctx, q, account, "")
	if err != nil {
		return nil, err
	}
	overrides, err := resourceOverrides(ctx, q, account)
	if err != nil {
		return nil, err
	}
	platform, err := platformResourceLimits(ctx, q)
	if err != nil {
		return nil, err
	}
	if input.Kind == "group" {
		for index := range groups {
			if groups[index].ID == input.TargetID {
				for dimension, value := range input.Dimensions {
					if value == nil {
						delete(groups[index].Dimensions, dimension)
					} else {
						groups[index].Dimensions[dimension] = *value
					}
				}
			}
		}
	} else if input.TargetID == account {
		for dimension, value := range input.Overrides {
			overrides[dimension] = value
		}
	}
	values, err := ResolveResources(groups, platform, overrides)
	if err != nil {
		return nil, err
	}
	if input.Kind == "user" {
		for dimension, value := range input.Overrides {
			if value != nil && *value > values[dimension].Ceiling {
				return nil, Admission("resource_above_ceiling:"+string(dimension), ErrInvalid)
			}
		}
	}
	return values, nil
}
func (s *Store) PreviewResourceChange(ctx context.Context, token string, input ResourceChange, cursor string, limit int) (ResourceChangePreview, error) {
	var out ResourceChangePreview
	if err := validateResourceChange(input); err != nil {
		return out, err
	}
	limit, err := adminLimit(limit)
	if err != nil {
		return out, err
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	if err = resourceTargetRevision(ctx, tx, input); err != nil {
		return out, err
	}
	out.ExpectedDependencies, err = resourceDependencies(ctx, tx, input)
	if err != nil {
		return out, err
	}
	scope := cursorScope("resource-change", []string{input.Kind, input.TargetID, out.ExpectedDependencies, creationDigest(input)})
	after, err := readAdminCursor(cursor, scope)
	if err != nil {
		return out, err
	}
	args := []any{input.Kind, input.TargetID, input.Kind, input.TargetID}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id IN (`+resourceAffectedAccounts+`)`, args...).Scan(&out.AffectedAccounts); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,username FROM accounts WHERE id IN (`+resourceAffectedAccounts+`) AND id>? ORDER BY id LIMIT ?`, append(args, after, limit+1)...)
	if err != nil {
		return out, err
	}
	out.Users = []ResourceImpact{}
	for rows.Next() {
		var user ResourceImpact
		if err = rows.Scan(&user.AccountID, &user.Username); err != nil {
			break
		}
		out.Users = append(out.Users, user)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if len(out.Users) > limit {
		out.Users = out.Users[:limit]
		out.NextCursor = nextAdminCursor(out.Users[limit-1].AccountID, scope)
	}
	platform, err := platformResourceLimits(ctx, tx)
	if err != nil {
		return out, err
	}
	for index := range out.Users {
		user := &out.Users[index]
		before, err := userResourceSettings(ctx, tx, user.AccountID)
		if err != nil {
			return out, err
		}
		user.Before = before.Effective
		user.After, err = proposedResources(ctx, tx, user.AccountID, input)
		if err != nil {
			return out, err
		}
		user.Usage, err = accountResourceUsage(ctx, tx, user.AccountID)
		if err != nil {
			return out, err
		}
		user.BeforeOverages, err = resourceOverages(user.Usage, user.Before)
		if err != nil {
			return out, err
		}
		user.AfterOverages, err = resourceOverages(user.Usage, user.After)
		if err != nil {
			return out, err
		}
		user.BeforeSavedPlans, err = savedPlanConflicts(ctx, tx, user.AccountID, user.Before)
		if err != nil {
			return out, err
		}
		user.AfterSavedPlans, err = savedPlanConflicts(ctx, tx, user.AccountID, user.After)
		if err != nil {
			return out, err
		}
		user.BeforePlan, err = selectedEnvironmentIssues(ctx, tx, user.AccountID, user.Before, platform[ResourceMounts])
		if err != nil {
			return out, err
		}
		user.AfterPlan, err = selectedEnvironmentIssues(ctx, tx, user.AccountID, user.After, platform[ResourceMounts])
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
func (s *Store) ApplyResourceChange(ctx context.Context, token string, input ResourceMutation) (ResourceChangeResult, error) {
	var out ResourceChangeResult
	if err := validateResourceChange(input.ResourceChange); err != nil {
		return out, err
	}
	request, err := mutationRequest(input.RequestID, "resource."+input.Kind+".update", input.TargetID, input)
	if err != nil {
		return out, err
	}
	changed := false
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, exists, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if exists {
			if err = json.Unmarshal(raw, &out); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return Admission("release_fenced", ErrDraining)
		}
		if err = resourceTargetRevision(ctx, tx, input.ResourceChange); err != nil {
			return err
		}
		dependencies, err := resourceDependencies(ctx, tx, input.ResourceChange)
		if err != nil {
			return err
		}
		if input.ExpectedDependencies == "" || dependencies != input.ExpectedDependencies {
			return Admission("preview_stale", ErrConflict)
		}
		details := map[string]any{}
		var revision int64
		if input.Kind == "group" {
			before, err := groupResourcePolicy(ctx, tx, input.TargetID)
			if err != nil {
				return err
			}
			details["before"] = before
			for dimension, value := range input.Dimensions {
				if value == nil {
					_, err = tx.ExecContext(ctx, `DELETE FROM group_resource_policy WHERE group_id=? AND dimension=?`, input.TargetID, dimension)
				} else {
					_, err = tx.ExecContext(ctx, `INSERT INTO group_resource_policy(group_id,dimension,default_value,ceiling) VALUES(?,?,?,?) ON CONFLICT(group_id,dimension) DO UPDATE SET default_value=excluded.default_value,ceiling=excluded.ceiling`, input.TargetID, dimension, value.Default, value.Ceiling)
				}
				if err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE account_groups SET revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), input.TargetID); err != nil {
				return err
			}
			if err = advanceGroupMembers(ctx, tx, input.TargetID); err != nil {
				return err
			}
			after, err := groupResourcePolicy(ctx, tx, input.TargetID)
			if err != nil {
				return err
			}
			out.Group = &after
			revision = after.Revision
			details["after"] = after
		} else {
			before, err := userResourceSettings(ctx, tx, input.TargetID)
			if err != nil {
				return err
			}
			details["before"] = before.Overrides
			if _, err = proposedResources(ctx, tx, input.TargetID, input.ResourceChange); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO account_resource_settings(account_id,revision,updated_at) VALUES(?,1,?) ON CONFLICT(account_id) DO UPDATE SET revision=revision+1,updated_at=excluded.updated_at`, input.TargetID, s.timestamp()); err != nil {
				return err
			}
			for dimension, value := range input.Overrides {
				if value == nil {
					_, err = tx.ExecContext(ctx, `DELETE FROM account_resource_overrides WHERE account_id=? AND dimension=?`, input.TargetID, dimension)
				} else {
					_, err = tx.ExecContext(ctx, `INSERT INTO account_resource_overrides(account_id,dimension,requested) VALUES(?,?,?) ON CONFLICT(account_id,dimension) DO UPDATE SET requested=excluded.requested`, input.TargetID, dimension, *value)
				}
				if err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1,revision=revision+1 WHERE id=?`, input.TargetID); err != nil {
				return err
			}
			after, err := userResourceSettings(ctx, tx, input.TargetID)
			if err != nil {
				return err
			}
			out.User = &after
			revision = after.Revision
			details["after"] = after.Overrides
		}
		if err = s.auditEvent(ctx, tx, actor.ID, request.Action, input.Kind, input.TargetID, request.ID, revision, details); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return ResourceChangeResult{}, fmt.Errorf("resource change: %w", err)
	}
	if changed {
		s.invalidateIdentities()
	}
	return out, nil
}
