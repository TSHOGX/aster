package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Group struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Revision    int64    `json:"revision"`
	MemberCount int64    `json:"member_count"`
	PolicyIDs   []string `json:"policy_ids"`
}
type GroupChange struct {
	RequestID        string  `json:"request_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	Name             *string `json:"name,omitempty"`
	Description      *string `json:"description,omitempty"`
}
type PolicyChange struct {
	RequestID        string                          `json:"request_id"`
	ExpectedRevision int64                           `json:"expected_revision"`
	Name             *string                         `json:"name,omitempty"`
	Description      *string                         `json:"description,omitempty"`
	Statements       *map[ProductAction]PolicyEffect `json:"statements,omitempty"`
}
type GroupBindings struct {
	RequestID        string   `json:"request_id"`
	ExpectedRevision int64    `json:"expected_revision"`
	PolicyIDs        []string `json:"policy_ids"`
}
type GroupMembers struct {
	RequestID        string   `json:"request_id"`
	ExpectedRevision int64    `json:"expected_revision"`
	Add              []string `json:"add_user_ids"`
	Remove           []string `json:"remove_user_ids"`
}
type GroupDelete struct {
	RequestID            string `json:"request_id"`
	ExpectedRevision     int64  `json:"expected_revision"`
	ExpectedDependencies string `json:"expected_dependencies"`
}
type PolicyResult struct {
	Group     *Group            `json:"group,omitempty"`
	Policy    *PermissionPolicy `json:"policy,omitempty"`
	DeletedID string            `json:"deleted_id,omitempty"`
	Replayed  bool              `json:"replayed"`
}
type PolicyPage struct {
	Policies   []PermissionPolicy `json:"policies"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

func readGroup(ctx context.Context, q queryer, id string) (g Group, err error) {
	var raw string
	err = missing(q.QueryRowContext(ctx, `SELECT g.id,g.name,g.description,g.revision,(SELECT count(*) FROM account_memberships WHERE group_id=g.id),(SELECT json_group_array(policy_id) FROM group_policy_bindings WHERE group_id=g.id) FROM account_groups g WHERE g.id=?`, id).Scan(&g.ID, &g.Name, &g.Description, &g.Revision, &g.MemberCount, &raw))
	if err == nil {
		err = json.Unmarshal([]byte(raw), &g.PolicyIDs)
		sort.Strings(g.PolicyIDs)
	}
	return
}
func readPolicy(ctx context.Context, q queryer, id string) (p PermissionPolicy, err error) {
	var raw string
	err = missing(q.QueryRowContext(ctx, `SELECT id,name,description,revision,statements FROM permission_policies WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.Description, &p.Revision, &raw))
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p.Statements)
	}
	return
}
func (s *Store) policyMutation(ctx context.Context, token string, request adminRequest, apply func(*sql.Tx, Account) (PolicyResult, error)) (PolicyResult, error) {
	var result PolicyResult
	changed := false
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, exists, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if exists {
			if err = json.Unmarshal(raw, &result); err != nil {
				return err
			}
			result.Replayed = true
			return nil
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return Admission("release_fenced", ErrDraining)
		}
		result, err = apply(tx, actor)
		if err != nil {
			return err
		}
		raw, err = json.Marshal(result)
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
		return PolicyResult{}, err
	}
	if changed {
		s.invalidateIdentities()
	}
	return result, nil
}
func metadataChange(name, description *string, creating bool) error {
	if creating && name == nil || name != nil && !validateDisplayName(*name) {
		return Admission("name_invalid", ErrInvalid)
	}
	if description != nil && (!utf8.ValidString(*description) || utf8.RuneCountInString(*description) > 1024 || strings.ContainsRune(*description, 0)) {
		return Admission("description_invalid", ErrInvalid)
	}
	return nil
}
func uniqueName(ctx context.Context, q queryer, table, name, id string) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE name=? AND id<>?`, name, id).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return Admission("name_taken", ErrConflict)
	}
	return nil
}
func advanceGroupMembers(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1,revision=revision+1 WHERE id IN (SELECT account_id FROM account_memberships WHERE group_id=?)`, id)
	return err
}
func validateIDs(ids []string, maximum int) error {
	if len(ids) > maximum {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}
func (s *Store) SaveGroup(ctx context.Context, token, id string, input GroupChange) (PolicyResult, error) {
	action := "group.update"
	if id == "" {
		action = "group.create"
	}
	request, err := mutationRequest(input.RequestID, action, id, input)
	if err != nil {
		return PolicyResult{}, err
	}
	return s.policyMutation(ctx, token, request, func(tx *sql.Tx, actor Account) (PolicyResult, error) {
		if err := metadataChange(input.Name, input.Description, id == ""); err != nil {
			return PolicyResult{}, err
		}
		before := Group{PolicyIDs: []string{}}
		var err error
		if id != "" {
			before, err = readGroup(ctx, tx, id)
			if err != nil {
				return PolicyResult{}, err
			}
			if before.Revision != input.ExpectedRevision {
				return PolicyResult{}, ErrConflict
			}
		} else if input.ExpectedRevision != 0 {
			return PolicyResult{}, ErrInvalid
		}
		after := before
		if id == "" {
			after.ID = uuid.NewString()
		}
		if input.Name != nil {
			after.Name = *input.Name
		}
		if input.Description != nil {
			after.Description = *input.Description
		}
		after.Revision++
		if err = uniqueName(ctx, tx, "account_groups", after.Name, after.ID); err != nil {
			return PolicyResult{}, err
		}
		if id == "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO account_groups(id,name,description,revision,created_at,updated_at) VALUES(?,?,?,?,?,?)`, after.ID, after.Name, after.Description, after.Revision, s.timestamp(), s.timestamp())
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE account_groups SET name=?,description=?,revision=?,updated_at=? WHERE id=?`, after.Name, after.Description, after.Revision, s.timestamp(), id)
		}
		if err != nil {
			return PolicyResult{}, err
		}
		if err = advanceGroupMembers(ctx, tx, after.ID); err != nil {
			return PolicyResult{}, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, action, "group", after.ID, request.ID, after.Revision, map[string]any{"before": before, "after": after})
		return PolicyResult{Group: &after}, err
	})
}
func (s *Store) SavePolicy(ctx context.Context, token, id string, input PolicyChange) (PolicyResult, error) {
	action := "policy.update"
	if id == "" {
		action = "policy.create"
	}
	request, err := mutationRequest(input.RequestID, action, id, input)
	if err != nil {
		return PolicyResult{}, err
	}
	return s.policyMutation(ctx, token, request, func(tx *sql.Tx, actor Account) (PolicyResult, error) {
		if err := metadataChange(input.Name, input.Description, id == ""); err != nil {
			return PolicyResult{}, err
		}
		before := PermissionPolicy{Statements: map[ProductAction]PolicyEffect{}}
		var err error
		if id != "" {
			before, err = readPolicy(ctx, tx, id)
			if err != nil {
				return PolicyResult{}, err
			}
			if before.Revision != input.ExpectedRevision {
				return PolicyResult{}, ErrConflict
			}
		} else if input.ExpectedRevision != 0 {
			return PolicyResult{}, ErrInvalid
		}
		after := before
		if id == "" {
			after.ID = uuid.NewString()
		}
		if input.Name != nil {
			after.Name = *input.Name
		}
		if input.Description != nil {
			after.Description = *input.Description
		}
		if input.Statements != nil {
			after.Statements = *input.Statements
		}
		if after.Statements == nil {
			after.Statements = map[ProductAction]PolicyEffect{}
		}
		if err = ValidateStatements(after.Statements); err != nil {
			return PolicyResult{}, err
		}
		after.Revision++
		if err = uniqueName(ctx, tx, "permission_policies", after.Name, after.ID); err != nil {
			return PolicyResult{}, err
		}
		raw, err := json.Marshal(after.Statements)
		if err != nil {
			return PolicyResult{}, err
		}
		if id == "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO permission_policies(id,name,description,revision,statements,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, after.ID, after.Name, after.Description, after.Revision, string(raw), s.timestamp(), s.timestamp())
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE permission_policies SET name=?,description=?,revision=?,statements=?,updated_at=? WHERE id=?`, after.Name, after.Description, after.Revision, string(raw), s.timestamp(), id)
		}
		if err != nil {
			return PolicyResult{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1,revision=revision+1 WHERE id IN (SELECT m.account_id FROM account_memberships m JOIN group_policy_bindings b ON b.group_id=m.group_id WHERE b.policy_id=?)`, after.ID); err != nil {
			return PolicyResult{}, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, action, "policy", after.ID, request.ID, after.Revision, map[string]any{"before": before, "after": after})
		return PolicyResult{Policy: &after}, err
	})
}
func (s *Store) BindGroupPolicies(ctx context.Context, token, id string, input GroupBindings) (PolicyResult, error) {
	request, err := mutationRequest(input.RequestID, "group.policies.update", id, input)
	if err != nil {
		return PolicyResult{}, err
	}
	return s.policyMutation(ctx, token, request, func(tx *sql.Tx, actor Account) (PolicyResult, error) {
		if err := validateIDs(input.PolicyIDs, 256); err != nil {
			return PolicyResult{}, err
		}
		before, err := readGroup(ctx, tx, id)
		if err != nil {
			return PolicyResult{}, err
		}
		if before.Revision != input.ExpectedRevision {
			return PolicyResult{}, ErrConflict
		}
		for _, policy := range input.PolicyIDs {
			if _, err = readPolicy(ctx, tx, policy); err != nil {
				return PolicyResult{}, Admission("policy_unavailable", ErrInvalid)
			}
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM group_policy_bindings WHERE group_id=?`, id); err != nil {
			return PolicyResult{}, err
		}
		for _, policy := range input.PolicyIDs {
			if _, err = tx.ExecContext(ctx, `INSERT INTO group_policy_bindings(group_id,policy_id) VALUES(?,?)`, id, policy); err != nil {
				return PolicyResult{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE account_groups SET revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), id); err != nil {
			return PolicyResult{}, err
		}
		if err = advanceGroupMembers(ctx, tx, id); err != nil {
			return PolicyResult{}, err
		}
		after, err := readGroup(ctx, tx, id)
		if err != nil {
			return PolicyResult{}, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "group", id, request.ID, after.Revision, map[string]any{"before": before.PolicyIDs, "after": after.PolicyIDs})
		return PolicyResult{Group: &after}, err
	})
}
func (s *Store) UpdateGroupMembers(ctx context.Context, token, id string, input GroupMembers) (PolicyResult, error) {
	request, err := mutationRequest(input.RequestID, "group.members.update", id, input)
	if err != nil {
		return PolicyResult{}, err
	}
	return s.policyMutation(ctx, token, request, func(tx *sql.Tx, actor Account) (PolicyResult, error) {
		if err := validateIDs(input.Add, 100); err != nil {
			return PolicyResult{}, err
		}
		if err := validateIDs(input.Remove, 100); err != nil {
			return PolicyResult{}, err
		}
		if len(input.Add)+len(input.Remove) == 0 {
			return PolicyResult{}, ErrInvalid
		}
		before, err := readGroup(ctx, tx, id)
		if err != nil {
			return PolicyResult{}, err
		}
		if before.Revision != input.ExpectedRevision {
			return PolicyResult{}, ErrConflict
		}
		added := map[string]bool{}
		actualAdd, actualRemove := []string{}, []string{}
		for _, user := range input.Add {
			added[user] = true
		}
		for _, user := range input.Remove {
			if added[user] {
				return PolicyResult{}, ErrInvalid
			}
		}
		for _, user := range append(append([]string{}, input.Add...), input.Remove...) {
			if _, err = readAccount(ctx, tx, "id", user); err != nil {
				return PolicyResult{}, Admission("account_unavailable", ErrInvalid)
			}
		}
		for _, user := range input.Add {
			var count, exists int
			if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(max(group_id=?),0) FROM account_memberships WHERE account_id=?`, id, user).Scan(&count, &exists); err != nil {
				return PolicyResult{}, err
			}
			if exists == 0 && count >= 256 {
				return PolicyResult{}, Admission("membership_limit", ErrInvalid)
			}
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO account_memberships(account_id,group_id) VALUES(?,?)`, user, id); err != nil {
				return PolicyResult{}, err
			}
			if exists == 0 {
				actualAdd = append(actualAdd, user)
			}
		}
		for _, user := range input.Remove {
			result, err := tx.ExecContext(ctx, `DELETE FROM account_memberships WHERE account_id=? AND group_id=?`, user, id)
			if err != nil {
				return PolicyResult{}, err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return PolicyResult{}, err
			}
			if count != 0 {
				actualRemove = append(actualRemove, user)
			}
		}
		for _, user := range append(append([]string{}, actualAdd...), actualRemove...) {
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1,revision=revision+1 WHERE id=?`, user); err != nil {
				return PolicyResult{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE account_groups SET revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), id); err != nil {
			return PolicyResult{}, err
		}
		after, err := readGroup(ctx, tx, id)
		if err != nil {
			return PolicyResult{}, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "group", id, request.ID, after.Revision, map[string]any{"added": actualAdd, "removed": actualRemove})
		return PolicyResult{Group: &after}, err
	})
}
func (s *Store) DeletePolicy(ctx context.Context, token, id string, input AccountRevision) (PolicyResult, error) {
	request, err := mutationRequest(input.RequestID, "policy.delete", id, input)
	if err != nil {
		return PolicyResult{}, err
	}
	return s.policyMutation(ctx, token, request, func(tx *sql.Tx, actor Account) (PolicyResult, error) {
		before, err := readPolicy(ctx, tx, id)
		if err != nil {
			return PolicyResult{}, err
		}
		if before.Revision != input.ExpectedRevision {
			return PolicyResult{}, ErrConflict
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM group_policy_bindings WHERE policy_id=?`, id).Scan(&count); err != nil {
			return PolicyResult{}, err
		}
		if count != 0 {
			return PolicyResult{}, Admission("policy_in_use", ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM permission_policies WHERE id=?`, id); err != nil {
			return PolicyResult{}, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "policy", id, request.ID, before.Revision, map[string]any{"before": before})
		return PolicyResult{DeletedID: id}, err
	})
}
func (s *Store) Group(ctx context.Context, token, id string) (Group, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return Group{}, err
	}
	return readGroup(ctx, s.reader, id)
}
func (s *Store) Policy(ctx context.Context, token, id string) (PermissionPolicy, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return PermissionPolicy{}, err
	}
	return readPolicy(ctx, s.reader, id)
}
func (s *Store) Policies(ctx context.Context, token, cursor string, limit int) (PolicyPage, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return PolicyPage{}, err
	}
	limit, err := adminLimit(limit)
	if err != nil {
		return PolicyPage{}, err
	}
	scope := cursorScope("policies", nil)
	after, err := readAdminCursor(cursor, scope)
	if err != nil {
		return PolicyPage{}, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,name,description,revision,statements FROM permission_policies WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return PolicyPage{}, err
	}
	defer rows.Close()
	result := PolicyPage{Policies: []PermissionPolicy{}}
	for rows.Next() {
		var p PermissionPolicy
		var raw string
		if err = rows.Scan(&p.ID, &p.Name, &p.Description, &p.Revision, &raw); err != nil {
			return result, err
		}
		if err = json.Unmarshal([]byte(raw), &p.Statements); err != nil {
			return result, err
		}
		result.Policies = append(result.Policies, p)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Policies) > limit {
		result.Policies = result.Policies[:limit]
		result.NextCursor = nextAdminCursor(result.Policies[limit-1].ID, scope)
	}
	return result, nil
}

func (s *Store) GroupMembers(ctx context.Context, token, id, cursor string, limit int) (AdminUserPage, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return AdminUserPage{}, err
	}
	if _, err := readGroup(ctx, s.reader, id); err != nil {
		return AdminUserPage{}, err
	}
	return s.AdminUsers(ctx, token, AdminUserQuery{GroupID: id, Cursor: cursor, Limit: limit})
}
