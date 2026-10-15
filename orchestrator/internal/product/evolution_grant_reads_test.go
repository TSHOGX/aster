package product

import (
	"errors"
	"testing"
)

func TestEvolutionGrantInspectionUsesEffectiveAuthorityAndAdminBoundary(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	user, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "member", Username: "reader", DisplayName: "Reader", Role: "user"})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, "reader", user.TemporaryPassword)
	must(t, err)
	_, member, err := s.ChangePassword(ctx, temporary, "", "permanent-reader-password", true)
	must(t, err)
	inspect := func(account string) EvolutionAccountGrants {
		r, err := s.EvolutionAccountGrants(ctx, token, StandardUsersGroup, account)
		must(t, err)
		return r
	}
	if _, err = s.EvolutionAccountGrants(ctx, member, StandardUsersGroup, admin.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("non-admin inspected grant", err)
	}
	initial := inspect(admin.ID)
	if len(initial.Grants) != 4 || !initial.Grants[0].Allowed || initial.Grants[3].Allowed {
		t.Fatal(initial)
	}
	grant := EvolutionGroupGrantInput{RequestID: "read", AccountID: user.User.ID, Action: "config.read", Effect: "allow"}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, grant)
	must(t, err)
	state := inspect(user.User.ID)
	if !state.Member || !state.Grants[0].Allowed || state.Grants[1].Allowed || state.Grants[0].Revision != 1 {
		t.Fatal(state)
	}
	_, err = s.db.Exec("DELETE FROM account_memberships WHERE account_id=?", user.User.ID)
	must(t, err)
	state = inspect(user.User.ID)
	if state.Member || state.Grants[0].Allowed || state.Grants[0].Effect != "allow" {
		t.Fatal("membership loss ignored", state)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "deny-admin", AccountID: admin.ID, Action: "config.read", Effect: "deny"})
	must(t, err)
	if state = inspect(admin.ID); state.Grants[0].Allowed {
		t.Fatal("deny ignored", state)
	}
	// Management survives a config.read deny so an administrator can repair it.
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "allow-admin", AccountID: admin.ID, Action: "config.read", Effect: "allow", ExpectedRevision: 1})
	must(t, err)
	if _, err = s.EvolutionAccountGrants(ctx, token, "missing", admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.EvolutionAccountGrants(ctx, "", StandardUsersGroup, admin.ID); err == nil {
		t.Fatal("anonymous read")
	}
}
