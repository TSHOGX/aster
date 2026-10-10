package product

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/auth"
)

type AccountCreate struct {
	RequestID   string    `json:"request_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	GroupIDs    *[]string `json:"group_ids,omitempty"`
}
type AccountEdit struct {
	RequestID        string  `json:"request_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	DisplayName      *string `json:"display_name,omitempty"`
	Role             *string `json:"role,omitempty"`
}
type AccountRevision struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type AdminOperation struct {
	ID         string `json:"id"`
	ActorID    string `json:"actor_id"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Total      int64  `json:"total_targets"`
	Completed  int64  `json:"completed_targets"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// The persisted receipt type deliberately has no credential field.
type adminAccountReceipt struct {
	User      AdminUser       `json:"user"`
	Operation *AdminOperation `json:"operation,omitempty"`
}
type AccountMutation struct {
	adminAccountReceipt
	Replayed          bool   `json:"replayed"`
	TemporaryPassword string `json:"temporary_password,omitempty"`
}
type adminRequest struct{ ID, Action, Target, Digest string }

func mutationRequest(id, action, target string, body any) (adminRequest, error) {
	if !validID(id) {
		return adminRequest{}, Admission("request_id_invalid", ErrInvalid)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return adminRequest{}, err
	}
	return adminRequest{id, action, target, fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}
func (s *Store) adminSession(ctx context.Context, q queryer, token string) (Account, error) {
	session, err := s.normalLogin(ctx, q, token)
	if err != nil {
		return Account{}, err
	}
	if session.Account.Role != "admin" {
		return Account{}, ErrForbidden
	}
	return session.Account, nil
}
func adminReceipt(ctx context.Context, q queryer, actor string, request adminRequest) (AccountMutation, bool, error) {
	var action, target, digest, raw string
	err := q.QueryRowContext(ctx, `SELECT action,target_id,body_digest,result FROM admin_receipts WHERE actor_id=? AND request_id=?`, actor, request.ID).Scan(&action, &target, &digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountMutation{}, false, nil
	}
	if err != nil {
		return AccountMutation{}, false, err
	}
	if action != request.Action || target != request.Target || digest != request.Digest {
		return AccountMutation{}, false, Admission("request_id_conflict", ErrConflict)
	}
	var receipt adminAccountReceipt
	if err = json.Unmarshal([]byte(raw), &receipt); err != nil {
		return AccountMutation{}, false, err
	}
	return AccountMutation{adminAccountReceipt: receipt, Replayed: true}, true, nil
}
func (s *Store) checkAdminReplay(ctx context.Context, token string, request adminRequest) (AccountMutation, bool, error) {
	actor, err := s.adminSession(ctx, s.reader, token)
	if err != nil {
		return AccountMutation{}, false, err
	}
	return adminReceipt(ctx, s.reader, actor.ID, request)
}
func (s *Store) mutateAccount(ctx context.Context, token string, request adminRequest, apply func(*sql.Tx, Account) (adminAccountReceipt, bool, error)) (AccountMutation, error) {
	var result AccountMutation
	invalidate := false
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		previous, replayed, err := adminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replayed {
			result = previous
			return nil
		}
		state, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != "open" {
			return Admission("release_fenced", ErrDraining)
		}
		receipt, changed, err := apply(tx, actor)
		if err != nil {
			return err
		}
		invalidate = changed
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO admin_receipts(actor_id,request_id,action,target_id,body_digest,result,created_at) VALUES(?,?,?,?,?,?,?)`, actor.ID, request.ID, request.Action, request.Target, request.Digest, string(raw), s.timestamp()); err != nil {
			return err
		}
		result = AccountMutation{adminAccountReceipt: receipt}
		return nil
	})
	if err != nil {
		return AccountMutation{}, err
	}
	if invalidate {
		s.invalidateIdentities()
	}
	return result, nil
}
func temporaryPassword() (string, string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := auth.HashPassword(password)
	return password, hash, err
}
func accountConfiguration(a Account) map[string]any {
	return map[string]any{"display_name": a.DisplayName, "role": a.Role, "status": a.Status, "must_change_password": a.MustChangePassword}
}
func (s *Store) auditEvent(ctx context.Context, tx *sql.Tx, actor, action, kind, target, request string, revision int64, details any) error {
	raw := []byte("{}")
	var err error
	if details != nil {
		raw, err = json.Marshal(details)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,target_kind,target_id,request_id,revision,details,created_at) VALUES(?,?,?,?,?,?,?,?)`, actor, action, kind, target, request, revision, string(raw), s.timestamp())
	return err
}
func protectLastAdmin(ctx context.Context, tx *sql.Tx, before Account, role, status string) error {
	if before.Role != "admin" || before.Status != "active" || role == "admin" && status == "active" {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE role='admin' AND status='active' AND id<>?`, before.ID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return Admission("last_admin", ErrConflict)
	}
	return nil
}
func (s *Store) CreateAccount(ctx context.Context, token string, input AccountCreate) (AccountMutation, error) {
	request, err := mutationRequest(input.RequestID, "account.create", input.Username, input)
	if err != nil {
		return AccountMutation{}, err
	}
	if result, exists, err := s.checkAdminReplay(ctx, token, request); err != nil || exists {
		return result, err
	}
	name, err := canonicalUsername(input.Username)
	if err != nil || !validateDisplayName(input.DisplayName) || input.Role != "admin" && input.Role != "user" {
		return AccountMutation{}, Admission("account_invalid", ErrInvalid)
	}
	groups := []string{StandardUsersGroup}
	if input.GroupIDs != nil {
		groups = append([]string{}, (*input.GroupIDs)...)
	}
	if len(groups) > 256 {
		return AccountMutation{}, Admission("membership_limit", ErrInvalid)
	}
	sort.Strings(groups)
	for i, id := range groups {
		if !validID(id) || i > 0 && id == groups[i-1] {
			return AccountMutation{}, Admission("group_ids_invalid", ErrInvalid)
		}
	}
	password, hash, err := temporaryPassword()
	if err != nil {
		return AccountMutation{}, err
	}
	result, err := s.mutateAccount(ctx, token, request, func(tx *sql.Tx, actor Account) (adminAccountReceipt, bool, error) {
		for _, id := range groups {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM account_groups WHERE id=?`, id).Scan(&count); err != nil {
				return adminAccountReceipt{}, false, err
			}
			if count != 1 {
				return adminAccountReceipt{}, false, Admission("group_unavailable", ErrInvalid)
			}
		}
		account, err := s.insertAccount(ctx, tx, name, input.DisplayName, input.Role, hash, true)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		for _, id := range groups {
			if _, err = tx.ExecContext(ctx, `INSERT INTO account_memberships(account_id,group_id) VALUES(?,?)`, account.ID, id); err != nil {
				return adminAccountReceipt{}, false, err
			}
		}
		user, err := adminUser(ctx, tx, account.ID)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "account", account.ID, request.ID, account.Revision, map[string]any{"after": accountConfiguration(account), "group_ids": groups})
		return adminAccountReceipt{User: user}, false, err
	})
	if err == nil && !result.Replayed {
		result.TemporaryPassword = password
	}
	return result, err
}
func (s *Store) UpdateAccount(ctx context.Context, token, id string, input AccountEdit) (AccountMutation, error) {
	request, err := mutationRequest(input.RequestID, "account.update", id, input)
	if err != nil {
		return AccountMutation{}, err
	}
	if result, exists, err := s.checkAdminReplay(ctx, token, request); err != nil || exists {
		return result, err
	}
	if !validID(id) || input.ExpectedRevision < 1 || input.DisplayName == nil && input.Role == nil || input.DisplayName != nil && !validateDisplayName(*input.DisplayName) || input.Role != nil && *input.Role != "admin" && *input.Role != "user" {
		return AccountMutation{}, Admission("account_invalid", ErrInvalid)
	}
	return s.mutateAccount(ctx, token, request, func(tx *sql.Tx, actor Account) (adminAccountReceipt, bool, error) {
		before, err := readAccount(ctx, tx, "id", id)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		if before.Revision != input.ExpectedRevision {
			return adminAccountReceipt{}, false, Admission("revision_conflict", ErrConflict)
		}
		name, role := before.DisplayName, before.Role
		if input.DisplayName != nil {
			name = *input.DisplayName
		}
		if input.Role != nil {
			role = *input.Role
		}
		if err = protectLastAdmin(ctx, tx, before.Account, role, before.Status); err != nil {
			return adminAccountReceipt{}, false, err
		}
		changed := role != before.Role
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET display_name=?,role=?,revision=revision+1,session_revision=session_revision+?,updated_at=? WHERE id=?`, name, role, changed, s.timestamp(), id); err != nil {
			return adminAccountReceipt{}, false, err
		}
		if changed {
			if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, id); err != nil {
				return adminAccountReceipt{}, false, err
			}
		}
		user, err := adminUser(ctx, tx, id)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "account", id, request.ID, user.Revision, map[string]any{"before": accountConfiguration(before.Account), "after": accountConfiguration(user.Account)})
		return adminAccountReceipt{User: user}, changed, err
	})
}
func (s *Store) ResetAccountPassword(ctx context.Context, token, id string, input AccountRevision) (AccountMutation, error) {
	request, err := mutationRequest(input.RequestID, "account.password.reset", id, input)
	if err != nil {
		return AccountMutation{}, err
	}
	if result, exists, err := s.checkAdminReplay(ctx, token, request); err != nil || exists {
		return result, err
	}
	if !validID(id) || input.ExpectedRevision < 1 {
		return AccountMutation{}, ErrInvalid
	}
	password, hash, err := temporaryPassword()
	if err != nil {
		return AccountMutation{}, err
	}
	result, err := s.mutateAccount(ctx, token, request, func(tx *sql.Tx, actor Account) (adminAccountReceipt, bool, error) {
		before, err := readAccount(ctx, tx, "id", id)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		if before.Revision != input.ExpectedRevision {
			return adminAccountReceipt{}, false, Admission("revision_conflict", ErrConflict)
		}
		after, err := s.replaceAccountPassword(ctx, tx, before.Account, hash, before.Role, before.Status)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		user, err := adminUser(ctx, tx, id)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, request.Action, "account", id, request.ID, after.Revision, map[string]any{"must_change_password": true, "session_revision": after.SessionRevision})
		return adminAccountReceipt{User: user}, true, err
	})
	if err == nil && !result.Replayed {
		result.TemporaryPassword = password
	}
	return result, err
}
func (s *Store) SuspendAccount(ctx context.Context, token, id string, input AccountRevision) (AccountMutation, error) {
	return s.changeAccountStatus(ctx, token, id, input, "disabled")
}
func (s *Store) EnableAccount(ctx context.Context, token, id string, input AccountRevision) (AccountMutation, error) {
	return s.changeAccountStatus(ctx, token, id, input, "active")
}
func (s *Store) changeAccountStatus(ctx context.Context, token, id string, input AccountRevision, status string) (AccountMutation, error) {
	action := "account.suspend"
	if status == "active" {
		action = "account.enable"
	}
	request, err := mutationRequest(input.RequestID, action, id, input)
	if err != nil {
		return AccountMutation{}, err
	}
	if result, exists, err := s.checkAdminReplay(ctx, token, request); err != nil || exists {
		return result, err
	}
	if !validID(id) || input.ExpectedRevision < 1 {
		return AccountMutation{}, ErrInvalid
	}
	return s.mutateAccount(ctx, token, request, func(tx *sql.Tx, actor Account) (adminAccountReceipt, bool, error) {
		before, err := readAccount(ctx, tx, "id", id)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		if before.Revision != input.ExpectedRevision {
			return adminAccountReceipt{}, false, Admission("revision_conflict", ErrConflict)
		}
		if before.Status == status {
			return adminAccountReceipt{}, false, Admission("account_state_conflict", ErrConflict)
		}
		if err = protectLastAdmin(ctx, tx, before.Account, before.Role, status); err != nil {
			return adminAccountReceipt{}, false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET status=?,revision=revision+1,session_revision=session_revision+1,updated_at=? WHERE id=?`, status, s.timestamp(), id); err != nil {
			return adminAccountReceipt{}, false, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE account_id=?`, id); err != nil {
			return adminAccountReceipt{}, false, err
		}
		operation := AdminOperation{ID: uuid.NewString(), ActorID: actor.ID, Action: action, TargetKind: "account", TargetID: id, State: "accepted", CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		// Enabling only restores identity; it never resumes any owned work or shares.
		if status == "active" {
			operation.State = "succeeded"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO admin_operations(id,actor_id,action,target_kind,target_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, operation.ID, actor.ID, action, "account", id, operation.State, operation.CreatedAt, operation.UpdatedAt); err != nil {
			return adminAccountReceipt{}, false, err
		}
		if status == "disabled" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO admin_operation_targets(operation_id,project_id) SELECT ?,id FROM projects WHERE owner_id=? AND state<>'deleted'`, operation.ID, id); err != nil {
				return adminAccountReceipt{}, false, err
			}
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_operation_targets WHERE operation_id=?`, operation.ID).Scan(&operation.Total); err != nil {
				return adminAccountReceipt{}, false, err
			}
		}
		user, err := adminUser(ctx, tx, id)
		if err != nil {
			return adminAccountReceipt{}, false, err
		}
		err = s.auditEvent(ctx, tx, actor.ID, action, "account", id, request.ID, user.Revision, map[string]any{"before": accountConfiguration(before.Account), "after": accountConfiguration(user.Account), "operation_id": operation.ID, "state": operation.State})
		return adminAccountReceipt{User: user, Operation: &operation}, true, err
	})
}
