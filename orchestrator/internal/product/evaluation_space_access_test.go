package product

import (
	"aster/contracts/appidentity"
	"errors"
	"testing"
)

func evaluationGrant(t *testing.T, s *Store, token string) string {
	t.Helper()
	challenge, _ := pkceChallenge(identityVerifier)
	callback := "https://example.com/evaluation-space/auth/callback"
	code, err := s.AdmitIdentityCode(ctx, token, "evaluation-space", callback, challenge)
	must(t, err)
	_, grant, err := s.ExchangeIdentityCode(ctx, "evaluation-space", callback, code, identityVerifier)
	must(t, err)
	return grant
}

func TestEvaluationSpaceGroupsFilterBeforePagingAndRecheckAuthority(t *testing.T) {
	s, admin := accountStore(t)
	adminToken := adminTestToken(t, s, admin)
	for _, id := range []string{"space-a", "space-b", "space-c", "space-hidden"} {
		_, err := s.db.Exec(`INSERT INTO account_groups(id,name,created_at,updated_at) VALUES(?,?,?,?)`, id, id, s.timestamp(), s.timestamp())
		must(t, err)
	}
	created, err := s.CreateAccount(ctx, adminToken, AccountCreate{RequestID: "viewer", Username: "viewer", DisplayName: "Viewer", Role: "user", GroupIDs: &[]string{"space-a", "space-b", "space-c"}})
	must(t, err)
	_, restricted, err := s.Authenticate(ctx, "viewer", created.TemporaryPassword)
	must(t, err)
	_, token, err := s.ChangePassword(ctx, restricted, "", "permanent-viewer-password", true)
	must(t, err)
	for _, id := range []string{"space-a", "space-b", "space-c", "space-hidden"} {
		_, err = s.SetEvolutionGroupGrant(ctx, adminToken, id, EvolutionGroupGrantInput{RequestID: "read-" + id, AccountID: created.User.ID, Action: "config.read", Effect: "allow"})
		must(t, err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "space-b", EvolutionGroupGrantInput{RequestID: "deny-b", AccountID: created.User.ID, Action: "config.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	grant := evaluationGrant(t, s, token)
	input := appidentity.EvaluationAccessRequest{Grant: grant, Action: "config.read", Search: "space", Limit: 1}
	page, err := s.EvaluationSpaceAccess(ctx, input)
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != "space-a" || !page.HasMore || page.NextAfter != "space-a" {
		t.Fatalf("bad filtered page: %+v", page)
	}
	if page.Subject != created.User.ID || page.Items[0].CanManage || page.Items[0].CanRun || page.Items[0].CanReadEvidence || page.Items[0].State != "unconfigured" {
		t.Fatalf("viewer gained capabilities: %+v", page)
	}
	input.After = page.NextAfter
	page, err = s.EvaluationSpaceAccess(ctx, input)
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != "space-c" || page.HasMore {
		t.Fatalf("hidden groups affected pagination: %+v", page)
	}
	for _, group := range []string{"space-b", "space-hidden", "space-missing"} {
		_, err = s.EvaluationSpaceAccess(ctx, appidentity.EvaluationAccessRequest{Grant: grant, GroupID: group, Action: "config.read", Limit: 1})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("foreign or missing group %s: %v", group, err)
		}
	}
	exact := appidentity.EvaluationAccessRequest{Grant: grant, GroupID: "space-a", Action: "config.manage", Limit: 1}
	if _, err = s.EvaluationSpaceAccess(ctx, exact); !errors.Is(err, ErrForbidden) {
		t.Fatal("viewer managed group", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "space-a", EvolutionGroupGrantInput{RequestID: "manage-a", AccountID: created.User.ID, Action: "config.manage", Effect: "allow"})
	must(t, err)
	page, err = s.EvaluationSpaceAccess(ctx, exact)
	must(t, err)
	if !page.Items[0].CanManage || page.Items[0].CanRun || page.Items[0].CanReadEvidence {
		t.Fatal("capabilities merged", page)
	}
	exact.Action = "workflow.edit"
	if _, err = s.EvaluationSpaceAccess(ctx, exact); !errors.Is(err, ErrForbidden) {
		t.Fatal("manager gained workflow editing", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "space-c", EvolutionGroupGrantInput{RequestID: "editor-c", AccountID: created.User.ID, Action: "workflow.edit", Effect: "allow"})
	must(t, err)
	editor := appidentity.EvaluationAccessRequest{Grant: grant, GroupID: "space-c", Action: "workflow.edit", Limit: 1}
	edited, err := s.EvaluationSpaceAccess(ctx, editor)
	must(t, err)
	if !edited.Items[0].CanEditWorkflows || edited.Items[0].CanManage || edited.Items[0].CanRun || edited.Items[0].CanReadEvidence {
		t.Fatal("editor gained other capabilities", edited)
	}
	exact.Action = "source.read"
	if _, err = s.EvaluationSpaceAccess(ctx, exact); !errors.Is(err, ErrForbidden) {
		t.Fatal("manager gained evidence", err)
	}
	// A previously returned cursor grants no continuing access to the next group.
	_, err = s.db.Exec("DELETE FROM account_memberships WHERE account_id=? AND group_id='space-c'", created.User.ID)
	must(t, err)
	if _, err = s.EvaluationSpaceAccess(ctx, editor); !errors.Is(err, ErrForbidden) {
		t.Fatal("editor retained removed membership", err)
	}
	page, err = s.EvaluationSpaceAccess(ctx, input)
	must(t, err)
	if len(page.Items) != 0 || page.HasMore || page.NextAfter != "" {
		t.Fatal("revoked page leaked group", page)
	}
	// Identity grants cannot be substituted by a central session or another client.
	_, studioGrant := identityExchange(t, s, adminToken)
	for _, wrong := range []string{studioGrant, token} {
		input.Grant = wrong
		if _, err = s.EvaluationSpaceAccess(ctx, input); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("foreign credential", err)
		}
	}
	input.Grant = grant
	must(t, s.RevokeIdentity(ctx, "evaluation-space", grant))
	if _, err = s.EvaluationSpaceAccess(ctx, input); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logout ignored", err)
	}
}

func TestEvaluationSpaceAdministratorStillNeedsExplicitEvidenceAndRespectsDeny(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	grant := evaluationGrant(t, s, token)
	input := appidentity.EvaluationAccessRequest{Grant: grant, GroupID: StandardUsersGroup, Action: "config.read", Limit: 1}
	result, err := s.EvaluationSpaceAccess(ctx, input)
	must(t, err)
	if !result.Items[0].CanManage || !result.Items[0].CanRun || result.Items[0].CanReadEvidence || result.Items[0].CanEditWorkflows {
		t.Fatal(result)
	}
	input.Action = "workflow.edit"
	if _, err = s.EvaluationSpaceAccess(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin edited workflows implicitly", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "editor", AccountID: admin.ID, Action: "workflow.edit", Effect: "allow"})
	must(t, err)
	_, err = s.EvaluationSpaceAccess(ctx, input)
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "deny-editor", AccountID: admin.ID, Action: "workflow.edit", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, err = s.EvaluationSpaceAccess(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin bypassed editor denial", err)
	}
	input.Action = "source.read"
	if _, err = s.EvaluationSpaceAccess(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin read evidence implicitly", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "evidence", AccountID: admin.ID, Action: "source.read", Effect: "allow"})
	must(t, err)
	_, err = s.EvaluationSpaceAccess(ctx, input)
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "deny", AccountID: admin.ID, Action: "config.read", Effect: "deny"})
	must(t, err)
	input.Action = "config.read"
	if _, err = s.EvaluationSpaceAccess(ctx, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin bypassed deny", err)
	}
}
