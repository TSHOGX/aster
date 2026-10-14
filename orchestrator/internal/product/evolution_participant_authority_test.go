package product

import "testing"

func TestEvolutionParticipantUsesSavedActorAndCurrentGrant(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	scope := ProjectionScope{StandardUsersGroup, pending.OperationID, 1, pending.DesiredDigest}
	for _, action := range []string{"reserve", "lookup", "adopt"} {
		subject, err := s.EvolutionParticipantActor(ctx, scope, action)
		must(t, err)
		if subject != admin.ID {
			t.Fatal("wrong operation actor", subject)
		}
	}
	if _, err = s.EvolutionParticipantActor(ctx, scope, "cancel"); err != ErrForbidden {
		t.Fatal("live operation cancelled", err)
	}
	changed := scope
	changed.Revision++
	if _, err = s.EvolutionParticipantActor(ctx, changed, "adopt"); err != ErrForbidden {
		t.Fatal("substituted scope", err)
	}
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "studio", pending.DesiredDigest, "offline"))
	if _, err = s.EvolutionParticipantActor(ctx, scope, "adopt"); err != ErrForbidden {
		t.Fatal("failed operation adopted", err)
	}
	subject, err := s.EvolutionParticipantActor(ctx, scope, "cancel")
	must(t, err)
	if subject != admin.ID {
		t.Fatal(subject)
	}
	must(t, s.RetryEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, EvolutionRetryInput{RequestID: "retry", ExpectedRevision: pending.DesiredRevision}))
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "config.manage", "deny", 0})
	must(t, err)
	if _, err = s.EvolutionParticipantActor(ctx, scope, "adopt"); err != ErrForbidden {
		t.Fatal("revoked actor still authorized", err)
	}
	allowed, err := s.AuthorizeEvolutionProjection(ctx, scope)
	must(t, err)
	if allowed {
		t.Fatal("evaluation dispatch ignored actor revocation")
	}
}
