package product

import "testing"

func TestEvolutionRetryReplayDoesNotRestartLaterFailure(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	fail := func() {
		must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evolution-space", pending.DesiredDigest, "catalog_rejected"))
	}
	fail()
	input := EvolutionRetryInput{"retry-once", 1}
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
	fail()
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
	current, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if current.State != "failed" {
		t.Fatal("replay dispatched later failure", current)
	}
	input.RequestID = "retry-again"
	input.ExpectedRevision = 2
	if err = s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input); err != ErrConflict {
		t.Fatal("stale retry", err)
	}
	input.ExpectedRevision = 1
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, input))
}
