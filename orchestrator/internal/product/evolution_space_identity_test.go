package product

import (
	"errors"
	"testing"
)

func TestEvolutionSpaceIdentityIsSeparateFromStudioAndRechecksGroupAccess(t *testing.T) {
	s, admin := accountStore(t)
	adminToken := adminTestToken(t, s, admin)
	user, password, err := createTestAccount(t, s, admin.ID, "editor", "Editor", "user")
	must(t, err)
	_, restricted, err := s.Authenticate(ctx, user.Username, password)
	must(t, err)
	_, token, err := s.ChangePassword(ctx, restricted, "", "permanent-editor-password", true)
	must(t, err)
	challenge, _ := pkceChallenge(identityVerifier)
	const callback = "https://example.com/evolution-space/auth/callback"
	admit := func() (string, error) {
		return s.AdmitIdentityCode(ctx, token, "evolution-space", callback, challenge)
	}
	if _, err = admit(); !errors.Is(err, ErrForbidden) {
		t.Fatal("ungranted application entrance", err)
	}
	must(t, grantFixture(s, ctx, adminToken, "account", user.ID, "library", "volume.read", "allow", 0))
	_, studioGrant := identityExchange(t, s, token)
	if _, err = admit(); !errors.Is(err, ErrForbidden) {
		t.Fatal("Studio access implied Evolution Space access", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "read", AccountID: user.ID, Action: "config.read", Effect: "allow"})
	must(t, err)
	code, err := admit()
	must(t, err)
	assertion, grant, err := s.ExchangeIdentityCode(ctx, "evolution-space", callback, code, identityVerifier)
	must(t, err)
	if len(assertion.Entitlements) != 1 || assertion.Entitlements[0] != EvolutionSpaceAccessEntitlement {
		t.Fatal("cross-service entitlement", assertion)
	}
	if _, err = s.IntrospectIdentity(ctx, "studio", grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("evaluation grant used by Studio", err)
	}
	if _, err = s.IntrospectIdentity(ctx, "evolution-space", studioGrant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("Studio grant used by evaluation", err)
	}
	code, err = admit()
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "deny", AccountID: user.ID, Action: "config.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if _, _, err = s.ExchangeIdentityCode(ctx, "evolution-space", callback, code, identityVerifier); !errors.Is(err, ErrForbidden) {
		t.Fatal("exchange ignored revoke", err)
	}
	if _, err = s.IntrospectIdentity(ctx, "evolution-space", grant); !errors.Is(err, ErrForbidden) {
		t.Fatal("introspection ignored revoke", err)
	}
	_, err = s.IntrospectIdentity(ctx, "studio", studioGrant)
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, adminToken, StandardUsersGroup, EvolutionGroupGrantInput{RequestID: "restore", AccountID: user.ID, Action: "config.read", Effect: "allow", ExpectedRevision: 2})
	must(t, err)
	_, err = s.IntrospectIdentity(ctx, "evolution-space", grant)
	must(t, err)
	_, err = s.db.Exec("DELETE FROM account_memberships WHERE account_id=? AND group_id=?", user.ID, StandardUsersGroup)
	must(t, err)
	if _, err = s.IntrospectIdentity(ctx, "evolution-space", grant); !errors.Is(err, ErrForbidden) {
		t.Fatal("membership loss ignored", err)
	}
	must(t, s.RevokeIdentity(ctx, "evolution-space", grant))
	if _, err = s.IntrospectIdentity(ctx, "studio", studioGrant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-app logout did not revoke central session", err)
	}
}

func TestEvolutionSpaceIdentityAdministratorGetsEvolutionSpaceAndModelEntitlements(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	challenge, _ := pkceChallenge(identityVerifier)
	callback := "https://example.com/evolution-space/auth/callback"
	code, err := s.AdmitIdentityCode(ctx, token, "evolution-space", callback, challenge)
	must(t, err)
	assertion, _, err := s.ExchangeIdentityCode(ctx, "evolution-space", callback, code, identityVerifier)
	must(t, err)
	if len(assertion.Entitlements) != 2 || assertion.Entitlements[0] != EvolutionSpaceAccessEntitlement || assertion.Entitlements[1] != EvolutionSpaceModelsEntitlement {
		t.Fatal(assertion)
	}
	if _, err = s.AdmitIdentityCode(ctx, token, "unknown", identityCallback, challenge); !errors.Is(err, ErrForbidden) {
		t.Fatal("unknown application borrowed Studio access", err)
	}
}
