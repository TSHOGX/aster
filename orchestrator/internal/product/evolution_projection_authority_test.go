package product

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

func TestProjectionAuthorityBindsCurrentOperation(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	digest := activationDigestFixture(1, false)
	config, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"save-scope", 0, activationConfigFixture(1, false)})
	must(t, err)
	scope := ProjectionScope{StandardUsersGroup, config.OperationID, 1, digest}
	allowed, err := s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if !allowed {
		t.Fatal("current operation denied")
	}
	for _, mutate := range []func(*ProjectionScope){func(s *ProjectionScope) { s.GroupID = "other" }, func(s *ProjectionScope) { s.OperationID = "other" }, func(s *ProjectionScope) { s.Revision = 2 }, func(s *ProjectionScope) { s.Digest = evolution.Digest([]byte("other")) }} {
		changed := scope
		mutate(&changed)
		allowed, err := s.AuthorizeEvolutionProjection(ctx, changed)
		must(t, err)
		if allowed {
			t.Fatal("scope substitution allowed", changed)
		}
	}
	must(t, s.FailEvolutionParticipant(ctx, token, config.OperationID, "evolution-space", digest, "offline"))
	allowed, err = s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("failed operation still authorized")
	}
}
