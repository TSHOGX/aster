package product

import (
	"errors"
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
