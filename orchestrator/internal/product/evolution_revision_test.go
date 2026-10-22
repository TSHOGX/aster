package product

import "testing"

func TestEvolutionRevisionReadRetainsContentAndChecksAuthority(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	config := activationConfigFixture(1, true)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, config})
	must(t, err)
	_, err = s.DisableEvolution(ctx, token, StandardUsersGroup, DisableEvolutionInput{"off", 1})
	must(t, err)
	read, err := s.EvolutionConfigurationRevision(ctx, token, StandardUsersGroup, 1)
	must(t, err)
	if read.OperationID != pending.OperationID || read.Digest != pending.DesiredDigest || !read.Configuration.AutoEnabled || read.Configuration.Budget != config.Budget {
		t.Fatal(read)
	}
	if _, err = s.EvolutionConfigurationRevision(ctx, token, "other", 1); err != ErrNotFound {
		t.Fatal("cross-group revision", err)
	}
	if _, err = s.EvolutionConfigurationRevision(ctx, "", StandardUsersGroup, 1); err == nil {
		t.Fatal("anonymous revision read")
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "config.read", "deny", 0})
	must(t, err)
	if _, err = s.EvolutionConfigurationRevision(ctx, token, StandardUsersGroup, 1); err != ErrForbidden {
		t.Fatal("revoked read allowed", err)
	}
}
