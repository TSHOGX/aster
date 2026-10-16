package product

import (
	"errors"
	"testing"
)

func TestStudioGrantInspectionUsesCurrentAccountAndGroupAuthority(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	user, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "reader", Username: "reader", DisplayName: "Reader", Role: "user"})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "reader", user.TemporaryPassword)
	must(t, err)
	_, member, err := s.ChangePassword(ctx, temporary, "", "permanent-reader-password", true)
	must(t, err)
	inspect := func(kind, subject, library string) StudioSubjectGrants {
		value, err := s.InspectStudioGrants(ctx, token, kind, subject, library)
		must(t, err)
		return value
	}
	for _, credential := range []string{"", member} {
		if _, err = s.InspectStudioGrants(ctx, credential, "account", admin.ID, "library"); err == nil {
			t.Fatal("non-administrator inspected Studio policy")
		}
	}
	initial := inspect("account", admin.ID, "library")
	if len(initial.Grants) != 6 || initial.DisplayName == "" {
		t.Fatal(initial)
	}
	for _, grant := range initial.Grants {
		if grant.Effect != "unset" || grant.Revision != 0 || grant.Allowed == nil || *grant.Allowed {
			t.Fatal("administration fabricated Studio authority", grant)
		}
	}
	must(t, grantFixture(s, ctx, token, "group", StandardUsersGroup, "library", "volume.review", "allow", 0))
	group := inspect("group", StandardUsersGroup, "library")
	if group.Grants[2].Allowed != nil || group.Grants[2].Effect != "allow" || group.Grants[2].Revision != 1 {
		t.Fatal("group policy fabricated per-account access", group)
	}
	current := inspect("account", user.User.ID, "library")
	if current.Grants[2].Effect != "unset" || !*current.Grants[2].Allowed {
		t.Fatal("inherited allowance missing", current)
	}
	must(t, grantFixture(s, ctx, token, "account", user.User.ID, "library", "volume.review", "deny", 0))
	if *inspect("account", user.User.ID, "library").Grants[2].Allowed {
		t.Fatal("direct deny ignored")
	}
	must(t, grantFixture(s, ctx, token, "account", user.User.ID, "library", "volume.review", "allow", 1))
	must(t, grantFixture(s, ctx, token, "group", StandardUsersGroup, "library", "volume.review", "deny", 1))
	current = inspect("account", user.User.ID, "library")
	if current.Grants[2].Effect != "allow" || current.Grants[2].Revision != 2 || *current.Grants[2].Allowed {
		t.Fatal("inherited deny ignored", current)
	}
	if *inspect("account", user.User.ID, "other-library").Grants[2].Allowed {
		t.Fatal("grant crossed Volume")
	}
	_, err = s.db.Exec("DELETE FROM account_memberships WHERE account_id=?", user.User.ID)
	must(t, err)
	if !*inspect("account", user.User.ID, "library").Grants[2].Allowed {
		t.Fatal("removed group denial still applied")
	}
	_, err = s.db.Exec("UPDATE accounts SET status='disabled' WHERE id=?", user.User.ID)
	must(t, err)
	if *inspect("account", user.User.ID, "library").Grants[2].Allowed {
		t.Fatal("disabled account remained authorized")
	}
	for _, kind := range []string{"account", "group"} {
		if _, err = s.InspectStudioGrants(ctx, token, kind, "missing", "library"); !errors.Is(err, ErrNotFound) {
			t.Fatal("missing subject", err)
		}
	}
	if _, err = s.InspectStudioGrants(ctx, token, "account", admin.ID, "invalid/id"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid Volume", err)
	}
}
