package product

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

func TestEvolutionSourceAuthorityCannotBeSelfDeclared(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	source := evolution.SourceBinding{ID: "corpus", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true, Exclusions: []string{"private"}}
	config := activationConfigFixture(1, true)
	config.Sources = []evolution.SourceBinding{source}
	input := EvolutionActivationInput{"activate", 0, config}
	if _, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, input); err != ErrForbidden {
		t.Fatal("self-declared source accepted", err)
	}
	policy := EvolutionSourcePolicyInput{"grant", 0, source}
	_, err := s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, policy)
	must(t, err)
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, policy)
	must(t, err)
	forged := input
	forged.Configuration.Sources = []evolution.SourceBinding{source}
	forged.Configuration.Sources[0].Exclusions = nil
	if _, err = s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, forged); err != ErrForbidden {
		t.Fatal("exclusions removed", err)
	}
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	scope := ProjectionScope{StandardUsersGroup, pending.OperationID, 1, pending.DesiredDigest}
	allowed, err := s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if !allowed {
		t.Fatal("current policy denied")
	}
	must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, "studio", pending.DesiredDigest))
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{"revoke", 1, source})
	must(t, err)
	allowed, err = s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("revoked source still authorizes projection")
	}
	if err = acceptEvolutionFixture(s, ctx, token, pending.OperationID, "evaluation-space", pending.DesiredDigest); err != ErrForbidden {
		t.Fatal("late receipt bypassed source revocation", err)
	}
	if _, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID); err != ErrForbidden {
		t.Fatal("settlement ignored revocation", err)
	}
	source.PolicyRevision = 3
	if _, err = s.SetEvolutionSourcePolicy(ctx, "", StandardUsersGroup, EvolutionSourcePolicyInput{"anonymous", 2, source}); err == nil {
		t.Fatal("anonymous policy change")
	}
}
