package product

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestAdminContentRequiresExplicitGrantAndCommittedAudit(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	owner, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "owner", Username: "owner", DisplayName: "Owner", Role: "user"})
	must(t, err)
	scope := Scope{Actor: owner.User.ID, ProjectID: "private"}
	_, err = s.CreateProject(ctx, scope.Actor, scope.ProjectID, "Private")
	must(t, err)
	_, err = s.CreateSession(ctx, scope, "conversation", "Private conversation")
	must(t, err)
	seedMessage(t, s, scope, "conversation", "first", "private content marker")
	seedMessage(t, s, scope, "conversation", "second", strings.Repeat("界", 20000))
	input := AdminConversationRequest{Purpose: "Support case 42", Limit: 1}
	_, err = s.AdminConversation(ctx, token, owner.User.ID, "conversation", input)
	wantError(t, err, ErrForbidden)
	_, err = s.Messages(ctx, Scope{Actor: admin.ID, ProjectID: scope.ProjectID}, "conversation", 0, 10)
	wantError(t, err, ErrNotFound)
	policyName, groupName := "Audited content", "Content reviewers"
	statements := map[ProductAction]PolicyEffect{ActionAdminContentRead: PolicyAllow}
	policy, err := s.SavePolicy(ctx, token, "", PolicyChange{RequestID: "content-policy", Name: &policyName, Statements: &statements})
	must(t, err)
	group, err := s.SaveGroup(ctx, token, "", GroupChange{RequestID: "content-group", Name: &groupName})
	must(t, err)
	bound, err := s.BindGroupPolicies(ctx, token, group.Group.ID, GroupBindings{RequestID: "content-binding", ExpectedRevision: group.Group.Revision, PolicyIDs: []string{policy.Policy.ID}})
	must(t, err)
	_, err = s.UpdateGroupMembers(ctx, token, group.Group.ID, GroupMembers{RequestID: "content-member", ExpectedRevision: bound.Group.Revision, Add: []string{admin.ID, owner.User.ID}})
	must(t, err)
	page, err := s.AdminConversation(ctx, token, owner.User.ID, "conversation", input)
	must(t, err)
	if len(page.Messages) != 1 || page.Messages[0].Content != "private content marker" || page.NextAfter != 1 || page.AuditID == 0 {
		t.Fatal(page)
	}
	var audit string
	must(t, s.reader.QueryRow(`SELECT details FROM audit_events WHERE id=? AND action='admin.content.read' AND target_id='conversation'`, page.AuditID).Scan(&audit))
	if strings.Contains(audit, "private content marker") || !strings.Contains(audit, "Support case 42") || !strings.Contains(audit, owner.User.ID) {
		t.Fatal(audit)
	}
	raw, err := json.Marshal(page)
	must(t, err)
	if strings.Contains(string(raw), "native") || strings.Contains(string(raw), "password") {
		t.Fatal("private engine/identity fields exposed")
	}
	input.After = page.NextAfter
	next, err := s.AdminConversation(ctx, token, owner.User.ID, "conversation", input)
	must(t, err)
	if len(next.Messages) != 1 || !next.Messages[0].Truncated || len([]rune(next.Messages[0].Content)) != 16384 || next.NextAfter != 0 {
		t.Fatal("inspection bounds", len(next.Messages), next.NextAfter)
	}
	_, err = s.AdminConversation(ctx, token, admin.ID, "conversation", input)
	wantError(t, err, ErrNotFound)
	_, temporaryToken, err := s.Authenticate(ctx, owner.User.Username, owner.TemporaryPassword)
	must(t, err)
	_, ordinaryToken, err := s.ChangePassword(ctx, temporaryToken, "", "ordinary-permanent-password", true)
	must(t, err)
	_, err = s.AdminConversation(ctx, ordinaryToken, owner.User.ID, "conversation", input)
	wantError(t, err, ErrForbidden)
	// Audit failure rolls back the read result as well as the audit insertion.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_content_audit BEFORE INSERT ON audit_events WHEN NEW.action='admin.content.read' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		return err
	}))
	denied, err := s.AdminConversation(ctx, token, owner.User.ID, "conversation", input)
	if err == nil || len(denied.Messages) != 0 || denied.Title != "" {
		t.Fatal("content escaped failed audit", denied, err)
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, `DROP TRIGGER reject_content_audit`); return err }))
	current, err := s.Policy(ctx, token, policy.Policy.ID)
	must(t, err)
	statements[ActionAdminContentRead] = PolicyDeny
	_, err = s.SavePolicy(ctx, token, current.ID, PolicyChange{RequestID: "revoke-content", ExpectedRevision: current.Revision, Statements: &statements})
	must(t, err)
	_, err = s.AdminConversation(ctx, token, owner.User.ID, "conversation", input)
	wantError(t, err, ErrForbidden)
}
