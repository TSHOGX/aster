package product

import (
	"errors"
	"strings"
	"testing"
)

func TestSourceResourceCatalogFiltersOwnershipDeletionAndEveryPage(t *testing.T) {
	s, scope, _, _, token, _ := corpusFixture(t)
	other, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "other-owner", Username: "other", DisplayName: "Other", Role: "user", GroupIDs: &[]string{"fixture-users"}})
	must(t, err)
	for _, item := range []struct{ id, owner string }{{"owned-extra", scope.Actor}, {"foreign-project", other.User.ID}} {
		_, err = s.db.Exec(`INSERT INTO projects(id,owner_id,title,creation_digest,created_at,updated_at) VALUES(?,?,?,?,?,?)`, item.id, item.owner, "Source catalog marker", item.id, s.timestamp(), s.timestamp())
		must(t, err)
		_, err = s.db.Exec(`INSERT INTO sessions(id,project_id,title,creation_digest,created_at,updated_at) VALUES(?,?,?,?,?,?)`, item.id+"-session", item.id, "Source session marker", item.id, s.timestamp(), s.timestamp())
		must(t, err)
	}
	for _, kind := range []string{"project", "session"} {
		first, err := s.EvolutionSourceResources(ctx, token, "fixture-users", kind, "", "", 1)
		must(t, err)
		if len(first.Items) != 1 || !first.HasMore {
			t.Fatal(first)
		}
		next, err := s.EvolutionSourceResources(ctx, token, "fixture-users", kind, "", first.NextAfter, 50)
		must(t, err)
		if len(next.Items) != 1 || next.HasMore || next.Items[0].ID == first.Items[0].ID {
			t.Fatal(next)
		}
		for _, item := range append(first.Items, next.Items...) {
			if item.ProjectID == "foreign-project" {
				t.Fatal("admin read foreign source", item)
			}
		}
		filtered, err := s.EvolutionSourceResources(ctx, token, "fixture-users", kind, "marker", "", 25)
		must(t, err)
		if len(filtered.Items) != 1 || filtered.Items[0].ProjectID != "owned-extra" {
			t.Fatal(filtered)
		}
	}
	_, err = s.db.Exec("UPDATE projects SET state='deleting' WHERE id='owned-extra'")
	must(t, err)
	for _, kind := range []string{"project", "session"} {
		page, err := s.EvolutionSourceResources(ctx, token, "fixture-users", kind, "marker", "", 25)
		must(t, err)
		if len(page.Items) != 0 || page.HasMore {
			t.Fatal("deleting resource leaked", page)
		}
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "deny-catalog", AccountID: scope.Actor, Action: "config.read", Effect: "deny"})
	must(t, err)
	if _, err = s.EvolutionSourceResources(ctx, token, "fixture-users", "project", "", "owned-extra", 25); !errors.Is(err, ErrForbidden) {
		t.Fatal("continuation ignored revoke", err)
	}
	if _, err = s.EvolutionSourceResources(ctx, "", "fixture-users", "project", "", "", 25); err == nil {
		t.Fatal("anonymous metadata read")
	}
}

func TestEvolutionAudienceDiscoveryUsesScopedMembershipAndCurrentAuthority(t *testing.T) {
	s, admin := accountStore(t)
	adminToken := adminTestToken(t, s, admin)
	for _, id := range []string{"audience-a", "audience-b", "audience-secret"} {
		_, err := s.db.Exec(`INSERT INTO account_groups(id,name,created_at,updated_at) VALUES(?,?,?,?)`, id, id, s.timestamp(), s.timestamp())
		must(t, err)
	}
	created, err := s.CreateAccount(ctx, adminToken, AccountCreate{RequestID: "manager", Username: "manager", DisplayName: "Manager", Role: "user", GroupIDs: &[]string{"audience-a", "audience-b"}})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "manager", created.TemporaryPassword)
	must(t, err)
	_, token, err := s.ChangePassword(ctx, temporary, "", "permanent-manager-password", true)
	must(t, err)
	for _, action := range []string{"config.read", "config.manage"} {
		_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "audience-a", EvolutionGroupGrantInput{RequestID: strings.ReplaceAll(action, ".", "-"), AccountID: created.User.ID, Action: action, Effect: "allow"})
		must(t, err)
	}
	permissions, err := s.EvolutionPermissions(ctx, token, "audience-a")
	must(t, err)
	if permissions.GroupName != "audience-a" || !permissions.CanManage || permissions.CanManageGrants || permissions.CanReadEvidence {
		t.Fatal("scoped manager gained unrelated access", permissions)
	}
	if _, err = s.Group(ctx, token, "audience-a"); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped metadata granted admin group access", err)
	}
	first, err := s.EvolutionAudienceGroups(ctx, token, "audience-a", "", 1)
	must(t, err)
	if len(first.Items) != 1 || first.Items[0].ID != "audience-a" || !first.HasMore {
		t.Fatal(first)
	}
	second, err := s.EvolutionAudienceGroups(ctx, token, "audience-a", first.NextAfter, 1)
	must(t, err)
	if len(second.Items) != 1 || second.Items[0].ID != "audience-b" || second.HasMore {
		t.Fatal("foreign group affected page/count", second)
	}
	if _, err = s.EvolutionAudienceGroups(ctx, token, "audience-secret", "", 25); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign group authorized discovery", err)
	}
	_, err = s.db.Exec("DELETE FROM account_memberships WHERE account_id=? AND group_id='audience-b'", created.User.ID)
	must(t, err)
	second, err = s.EvolutionAudienceGroups(ctx, token, "audience-a", first.NextAfter, 1)
	must(t, err)
	if len(second.Items) != 0 || second.HasMore {
		t.Fatal("revoked membership remained", second)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, "audience-a", EvolutionGroupGrantInput{RequestID: "deny-read", AccountID: created.User.ID, Action: "config.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, err = s.EvolutionAudienceGroups(ctx, token, "audience-a", first.NextAfter, 25); !errors.Is(err, ErrForbidden) {
		t.Fatal("continuation bypassed denial", err)
	}
	if value, err := s.EvolutionPermissions(ctx, token, "audience-a"); !errors.Is(err, ErrForbidden) || value.GroupName != "" {
		t.Fatal("denied group name leaked", value, err)
	}
}
