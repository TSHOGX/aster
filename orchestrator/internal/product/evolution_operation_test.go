package product

import "testing"

func TestEvolutionOperationInspectionScopeAndFailure(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evaluation-space", pending.DesiredDigest, "catalog_rejected"))
	value, err := s.EvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID)
	must(t, err)
	if value.State != "failed" || len(value.Participants) != 1 || value.Participants[0].Error != "catalog_rejected" || value.Digest != pending.DesiredDigest {
		t.Fatal(value)
	}
	if _, err = s.EvolutionOperation(ctx, "", StandardUsersGroup, pending.OperationID); err == nil {
		t.Fatal("anonymous inspection")
	}
	if _, err = s.EvolutionOperation(ctx, token, "other", pending.OperationID); err != ErrNotFound {
		t.Fatal("cross-group operation returned", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "config.read", "deny", 0})
	must(t, err)
	if _, err = s.EvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID); err != ErrForbidden {
		t.Fatal("revoked read allowed", err)
	}
}
