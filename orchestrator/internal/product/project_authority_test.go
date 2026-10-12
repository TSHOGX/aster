package product

import (
	"errors"
	"testing"
)

func TestProjectActionsDenyButRetainOwnedCleanup(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	scope := Scope{Actor: admin.ID, ProjectID: "owned"}
	p, err := s.CreateProject(ctx, admin.ID, scope.ProjectID, "Owned")
	must(t, err)
	session, err := s.CreateSession(ctx, scope, "session", "Session")
	must(t, err)
	terminal, err := s.CreateTerminal(ctx, scope, "terminal", "Terminal", 24, 80)
	must(t, err)
	other, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "other", Username: "other", DisplayName: "Other", Role: "user"})
	must(t, err)
	foreign := Scope{Actor: other.User.ID, ProjectID: "foreign"}
	_, err = s.CreateProject(ctx, foreign.Actor, foreign.ProjectID, "Foreign")
	must(t, err)
	_, err = s.CreateSession(ctx, foreign, "foreign-session", "Foreign session")
	must(t, err)
	must(t, s.GrantConversation(ctx, foreign, "foreign-session", admin.ID))
	policy, err := s.Policy(ctx, token, standardUsersPolicy)
	must(t, err)
	deny := map[ProductAction]PolicyEffect{ActionProjectsRead: PolicyDeny, ActionProjectsCreate: PolicyDeny, ActionProjectsManage: PolicyDeny, ActionAgentsRun: PolicyDeny, ActionTerminalsUse: PolicyDeny}
	_, err = s.SavePolicy(ctx, token, policy.ID, PolicyChange{RequestID: "deny", ExpectedRevision: policy.Revision, Statements: &deny})
	must(t, err)
	_, err = s.Project(ctx, scope)
	wantError(t, err, ErrForbidden)
	_, err = s.Projects(ctx, admin.ID, "", 10)
	wantError(t, err, ErrForbidden)
	_, err = s.Project(ctx, Scope{Actor: admin.ID, ProjectID: foreign.ProjectID})
	wantError(t, err, ErrNotFound)
	_, err = s.Session(ctx, scope, "foreign-session")
	wantError(t, err, ErrNotFound)
	wantError(t, s.RenameSession(ctx, scope, "foreign-session", 1, "Forbidden"), ErrNotFound)
	wantError(t, s.CanReadConversation(ctx, admin.ID, session.ID), ErrForbidden)
	must(t, s.CanReadConversation(ctx, admin.ID, "foreign-session"))
	matches, err := s.Search(ctx, admin.ID, "Session", "", 50)
	must(t, err)
	for _, match := range matches {
		if match.ProjectID == scope.ProjectID {
			t.Fatal("search bypassed own read denial")
		}
	}
	_, err = s.Messages(ctx, scope, session.ID, 0, 10)
	wantError(t, err, ErrForbidden)
	_, err = s.Turns(ctx, scope, session.ID, 0, 10)
	wantError(t, err, ErrForbidden)
	_, err = s.CreateProject(ctx, admin.ID, "new", "Denied")
	wantError(t, err, ErrForbidden)
	_, err = s.CreateProject(ctx, admin.ID, foreign.ProjectID, "Foreign")
	wantError(t, err, ErrNotFound)
	_, err = s.CreateSession(ctx, scope, "new-session", "Denied")
	wantError(t, err, ErrForbidden)
	_, err = s.CreateTerminal(ctx, scope, "new-terminal", "Denied", 24, 80)
	wantError(t, err, ErrForbidden)
	wantError(t, s.RenameProject(ctx, scope, p.Revision, "Denied"), ErrForbidden)
	must(t, s.DeleteSession(ctx, scope, session.ID, session.Revision))
	must(t, s.DeleteTerminal(ctx, scope, terminal.ID, terminal.Revision))
	current, err := ownedProject(ctx, s.reader, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, current.Revision))
	current, err = ownedProject(ctx, s.reader, scope)
	must(t, err)
	if current.State != "deleting" {
		t.Fatal("cleanup lost authority")
	}
}
func TestProjectCreationRequiresCurrentAccountAndGroupGrant(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	if _, err := s.CreateProject(ctx, "unknown-identity", "missing", "Missing"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unknown identity admitted", err)
	}
	none := []string{}
	user, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "no-groups", Username: "none", DisplayName: "None", Role: "user", GroupIDs: &none})
	must(t, err)
	_, err = s.CreateProject(ctx, user.User.ID, "no-grant", "Denied")
	wantError(t, err, ErrForbidden)
	group, err := s.Group(ctx, token, StandardUsersGroup)
	must(t, err)
	_, err = s.UpdateGroupMembers(ctx, token, group.ID, GroupMembers{RequestID: "membership", ExpectedRevision: group.Revision, Add: []string{user.User.ID}})
	must(t, err)
	_, err = s.CreateProject(ctx, user.User.ID, "permitted", "Permitted")
	must(t, err)
	latest, err := s.Account(ctx, user.User.ID)
	must(t, err)
	_, err = s.SuspendAccount(ctx, token, user.User.ID, AccountRevision{RequestID: "disable", ExpectedRevision: latest.Revision})
	must(t, err)
	_, err = s.CreateProject(ctx, user.User.ID, "disabled", "Denied")
	wantError(t, err, ErrForbidden)
}
