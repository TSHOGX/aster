package product

import (
	"aster/contracts/evolution"
	"testing"
)

func TestEvolutionSourceDiscoveryPaginationAndRevocation(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	_, err := s.CreateProject(ctx, admin.ID, "project", "Source")
	must(t, err)
	for _, id := range []string{"alpha", "beta", "gamma"} {
		source := evolution.SourceBinding{ID: id, Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{StandardUsersGroup}, DerivativePublication: true}
		_, err = s.SetEvolutionSourcePolicy(ctx, token, StandardUsersGroup, EvolutionSourcePolicyInput{id, 0, source})
		must(t, err)
	}
	first, err := s.EvolutionSources(ctx, token, StandardUsersGroup, "", 2)
	must(t, err)
	if len(first.Sources) != 2 || !first.HasMore || first.NextAfter != "beta" {
		t.Fatal(first)
	}
	next, err := s.EvolutionSources(ctx, token, StandardUsersGroup, first.NextAfter, 2)
	must(t, err)
	if len(next.Sources) != 1 || next.HasMore || next.Sources[0].ID != "gamma" {
		t.Fatal(next)
	}
	empty, err := s.EvolutionSources(ctx, token, "other", "", 2)
	must(t, err)
	if len(empty.Sources) != 0 {
		t.Fatal("cross-group policies listed")
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "config.read", "deny", 0})
	must(t, err)
	if _, err = s.EvolutionSources(ctx, token, StandardUsersGroup, first.NextAfter, 2); err != ErrForbidden {
		t.Fatal("cursor bypassed revocation", err)
	}
}
