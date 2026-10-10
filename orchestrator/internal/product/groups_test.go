package product

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestGroupPolicyPersistenceDenyPrecedenceAndRevision(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	initial, err := s.Capabilities(ctx, token)
	must(t, err)
	if !initial.Actions[ActionFilesRead].Allowed || initial.Actions[ActionPublicSharesCreate].Allowed || initial.Actions[ActionAdminContentRead].Allowed {
		t.Fatal(initial)
	}
	must(t, authorizeAction(ctx, s.reader, admin.ID, ActionFilesRead))
	if err = authorizeAction(ctx, s.reader, admin.ID, ActionPublicSharesCreate); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	name := "Restricted files"
	group, err := s.SaveGroup(ctx, token, "", GroupChange{RequestID: "group", Name: &name})
	must(t, err)
	statements := map[ProductAction]PolicyEffect{ActionFilesRead: PolicyDeny, ActionFilesExport: PolicyUnset}
	policy, err := s.SavePolicy(ctx, token, "", PolicyChange{RequestID: "policy", Name: &name, Statements: &statements})
	must(t, err)
	bound, err := s.BindGroupPolicies(ctx, token, group.Group.ID, GroupBindings{RequestID: "bind", ExpectedRevision: group.Group.Revision, PolicyIDs: []string{policy.Policy.ID}})
	must(t, err)
	changes := s.IdentityInvalidations()
	added, err := s.UpdateGroupMembers(ctx, token, group.Group.ID, GroupMembers{RequestID: "add", ExpectedRevision: bound.Group.Revision, Add: []string{admin.ID}})
	must(t, err)
	select {
	case <-changes:
	default:
		t.Fatal("membership commit did not invalidate authority")
	}
	current, err := s.Capabilities(ctx, token)
	must(t, err)
	if current.Revision <= initial.Revision || current.Actions[ActionFilesRead].Allowed || len(current.Actions[ActionFilesRead].Sources) != 2 {
		t.Fatal(current)
	}
	if err = authorizeAction(ctx, s.reader, admin.ID, ActionFilesRead); !errors.Is(err, ErrForbidden) {
		t.Fatal("SQL admission ignored deny", err)
	}
	replay, err := s.UpdateGroupMembers(ctx, token, group.Group.ID, GroupMembers{RequestID: "add", ExpectedRevision: bound.Group.Revision, Add: []string{admin.ID}})
	must(t, err)
	if !replay.Replayed || replay.Group.Revision != added.Group.Revision {
		t.Fatal(replay)
	}
	beforeRevision := current.Revision
	allow := map[ProductAction]PolicyEffect{ActionFilesRead: PolicyAllow}
	updated, err := s.SavePolicy(ctx, token, policy.Policy.ID, PolicyChange{RequestID: "allow", ExpectedRevision: policy.Policy.Revision, Statements: &allow})
	must(t, err)
	current, err = s.Capabilities(ctx, token)
	must(t, err)
	if current.Revision <= beforeRevision || !current.Actions[ActionFilesRead].Allowed {
		t.Fatal(current)
	}
	must(t, authorizeAction(ctx, s.reader, admin.ID, ActionFilesRead))
	if _, err = s.DeletePolicy(ctx, token, updated.Policy.ID, AccountRevision{RequestID: "delete-bound", ExpectedRevision: updated.Policy.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal("bound policy deleted", err)
	}
	unknown := map[ProductAction]PolicyEffect{"files.*": PolicyAllow}
	if _, err = s.SavePolicy(ctx, token, policy.Policy.ID, PolicyChange{RequestID: "wildcard", ExpectedRevision: updated.Policy.Revision, Statements: &unknown}); !errors.Is(err, ErrInvalid) {
		t.Fatal("wildcard accepted", err)
	}
}
func TestGroupDeletionPreviewConflictsAndPreservesAdminRole(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	user, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "member", Username: "member", DisplayName: "Member", Role: "user"})
	must(t, err)
	preview, err := s.PreviewGroupDeletion(ctx, token, StandardUsersGroup, "", 1)
	must(t, err)
	if preview.Group.MemberCount != 2 || len(preview.Users) != 1 || preview.NextCursor == "" {
		t.Fatal(preview)
	}
	for _, impact := range preview.Users {
		if impact.BeforeResources[ResourceProjects].Effective != 10 || impact.AfterResources[ResourceProjects].Effective != 0 || !impact.BeforeAccess[ActionFilesRead].Allowed || impact.AfterAccess[ActionFilesRead].Allowed {
			t.Fatal(impact)
		}
	}
	next, err := s.PreviewGroupDeletion(ctx, token, StandardUsersGroup, preview.NextCursor, 1)
	must(t, err)
	if len(next.Users) != 1 || next.Users[0].AccountID == preview.Users[0].AccountID {
		t.Fatal(next)
	}
	if _, err = s.DeleteGroup(ctx, token, StandardUsersGroup, GroupDelete{RequestID: "no-preview", ExpectedRevision: preview.Group.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	name := "Another"
	other, err := s.SaveGroup(ctx, token, "", GroupChange{RequestID: "other", Name: &name})
	must(t, err)
	_, err = s.UpdateGroupMembers(ctx, token, other.Group.ID, GroupMembers{RequestID: "other-member", ExpectedRevision: other.Group.Revision, Add: []string{user.User.ID}})
	must(t, err)
	if _, err = s.DeleteGroup(ctx, token, StandardUsersGroup, GroupDelete{RequestID: "stale-preview", ExpectedRevision: preview.Group.Revision, ExpectedDependencies: preview.ExpectedDependencies}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale preview committed", err)
	}
	fresh, err := s.PreviewGroupDeletion(ctx, token, StandardUsersGroup, "", 100)
	must(t, err)
	input := GroupDelete{RequestID: "delete", ExpectedRevision: fresh.Group.Revision, ExpectedDependencies: fresh.ExpectedDependencies}
	deleted, err := s.DeleteGroup(ctx, token, StandardUsersGroup, input)
	must(t, err)
	replay, err := s.DeleteGroup(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if !replay.Replayed || replay.DeletedID != deleted.DeletedID {
		t.Fatal(replay)
	}
	current, err := s.Capabilities(ctx, token)
	must(t, err)
	if current.Actions[ActionProjectsCreate].Allowed {
		t.Fatal("membership replaced implicitly")
	}
	account, err := s.Account(ctx, admin.ID)
	must(t, err)
	if account.Role != "admin" || account.Status != "active" {
		t.Fatal("group deletion changed protected role")
	}
	if _, err = s.AdminUsers(ctx, token, AdminUserQuery{}); err != nil {
		t.Fatal("administration depends on product group", err)
	}
}
func TestProductAuthorityIsIndependentOfBrowserCredentialSetup(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	user, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: uuid.NewString(), Username: "worker", DisplayName: "Worker", Role: "user"})
	must(t, err)
	// A temporary/reset credential restricts browser sessions, not admitted work.
	must(t, authorizeAction(ctx, s.reader, user.User.ID, ActionAgentsRun))
	if err = authorizeAction(ctx, s.reader, "missing-identity", ActionAgentsRun); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("missing identity bypass", err)
	}
	_, err = s.SuspendAccount(ctx, token, user.User.ID, AccountRevision{RequestID: "suspend", ExpectedRevision: user.User.Revision})
	must(t, err)
	if err = authorizeAction(ctx, s.reader, user.User.ID, ActionAgentsRun); !errors.Is(err, ErrForbidden) {
		t.Fatal("disabled account admitted", err)
	}
	access, err := s.AdminEffectiveAccess(ctx, token, user.User.ID)
	must(t, err)
	if access.AccountStatus != "disabled" || access.Actions[ActionAgentsRun].Allowed {
		t.Fatal(access)
	}
}
