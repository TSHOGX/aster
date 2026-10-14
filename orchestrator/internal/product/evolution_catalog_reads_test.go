package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"testing"
)

func TestGroupCatalogOnlyReturnsBoundDefinitions(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	body := catalogTemplateBody("opaque")
	_, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "bound", Revision: 1, Digest: evolution.Digest(body), Body: body, Enabled: true})
	must(t, err)
	if _, err := s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "bound", 1); err != nil {
		t.Fatal(err)
	}
	values, err := s.GroupCatalog(ctx, token, StandardUsersGroup)
	must(t, err)
	if len(values) != 1 || values[0].CatalogID != "bound" {
		t.Fatal(values)
	}
	if _, err = s.GroupCatalog(ctx, "", StandardUsersGroup); err == nil {
		t.Fatal("unauthenticated group catalog")
	}
}

func TestGroupCatalogAllowsOrdinaryMemberAndRejectsRemovedMembership(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	adminToken := adminTestToken(t, s, admin)
	created, err := s.CreateAccount(ctx, adminToken, AccountCreate{RequestID: "member", Username: "catalog-member", DisplayName: "Member", Role: "user"})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "catalog-member", created.TemporaryPassword)
	must(t, err)
	if _, err = s.GroupCatalog(ctx, temporary, StandardUsersGroup); err == nil {
		t.Fatal("restricted session read catalog")
	}
	_, token, err := s.ChangePassword(ctx, temporary, "", "permanent-member-password", true)
	must(t, err)
	if _, err = s.GroupCatalog(ctx, token, StandardUsersGroup); err != nil {
		t.Fatal("ordinary member denied", err)
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM account_memberships WHERE account_id=? AND group_id=?", created.User.ID, StandardUsersGroup)
		return err
	}))
	if _, err = s.GroupCatalog(ctx, token, StandardUsersGroup); err != ErrForbidden {
		t.Fatal("removed member retained discovery", err)
	}
}
