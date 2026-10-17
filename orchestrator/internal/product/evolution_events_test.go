package product

import "testing"

func TestEvolutionEventsPaginationAndReplay(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evolution-space", pending.DesiredDigest, "catalog_rejected"))
	input := EvolutionRetryInput{"retry", 1}
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
	first, err := s.EvolutionOperationEvents(ctx, token, StandardUsersGroup, pending.OperationID, 0, 2)
	must(t, err)
	if len(first.Events) != 2 || !first.HasMore || first.NextAfter != 2 || first.Events[0].State != "activating" || first.Events[1].State != "failed" {
		t.Fatal(first)
	}
	next, err := s.EvolutionOperationEvents(ctx, token, StandardUsersGroup, pending.OperationID, first.NextAfter, 2)
	must(t, err)
	if len(next.Events) != 1 || next.HasMore || next.Events[0].State != "activating" || next.NextAfter != 3 {
		t.Fatal("replay added event", next)
	}
	if _, err = s.EvolutionOperationEvents(ctx, token, "other", pending.OperationID, 0, 2); err != ErrNotFound {
		t.Fatal("cross-group event leak", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "config.read", "deny", 0})
	must(t, err)
	if _, err = s.EvolutionOperationEvents(ctx, token, StandardUsersGroup, pending.OperationID, 2, 2); err != ErrForbidden {
		t.Fatal("cursor bypassed revoked authority", err)
	}
}
