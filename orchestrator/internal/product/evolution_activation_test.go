package product

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

func TestEvolutionActivationDesiredActiveReceipts(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	group := StandardUsersGroup
	current, err := s.EvolutionConfig(ctx, token, group)
	if err == nil || current.GroupID != "" {
		t.Fatal("unexpected uninitialized config", current, err)
	}
	desired, err := s.SaveEvolutionConfig(ctx, token, group, EvolutionActivationInput{"config", 0, evolution.Digest([]byte("config")), true})
	must(t, err)
	if desired.State != "activating" || desired.ActiveRevision != 0 {
		t.Fatal(desired)
	}
	must(t, s.AcceptEvolutionParticipant(ctx, token, desired.OperationID, "evaluation-space", evolution.Digest([]byte("config"))))
	if _, err := s.SettleEvolutionActivation(ctx, token, desired.OperationID); err != ErrBusy {
		t.Fatal("one receipt settled", err)
	}
	must(t, s.AcceptEvolutionParticipant(ctx, token, desired.OperationID, "studio", evolution.Digest([]byte("config"))))
	active, err := s.SettleEvolutionActivation(ctx, token, desired.OperationID)
	must(t, err)
	if active.State != "active" || active.ActiveRevision != 1 || active.ActiveDigest != evolution.Digest([]byte("config")) {
		t.Fatal(active)
	}
	if _, err := s.SaveEvolutionConfig(ctx, token, group, EvolutionActivationInput{"stale", 0, evolution.Digest([]byte("next")), false}); err != ErrConflict {
		t.Fatal("stale desired revision accepted", err)
	}
}

func TestEvolutionActivationAuthorizationReplayAndParticipantIdentity(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input := EvolutionActivationInput{"save-once", 0, evolution.Digest([]byte("digest")), false}
	first, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	again, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if first != again {
		t.Fatal("retry changed operation", first, again)
	}
	if _, err = s.EvolutionConfig(ctx, "", StandardUsersGroup); err == nil {
		t.Fatal("unauthenticated config read")
	}
	if err = s.AcceptEvolutionParticipant(ctx, token, first.OperationID, "invented", evolution.Digest([]byte("digest"))); err != ErrInvalid {
		t.Fatal("unknown participant accepted", err)
	}
	if _, err = s.SettleEvolutionActivation(ctx, token, first.OperationID); err != ErrBusy {
		t.Fatal("missing required receipts settled", err)
	}
}

func TestEvolutionActivationPendingCannotBeOverwritten(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	digest := evolution.Digest([]byte("config"))
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"first", 0, digest, true})
	must(t, err)
	if _, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"second", 1, evolution.Digest([]byte("other")), false}); err != ErrBusy {
		t.Fatal("pending operation overwritten", err)
	}
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, s.AcceptEvolutionParticipant(ctx, token, pending.OperationID, participant, digest))
	}
	first, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	again, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	if first != again {
		t.Fatal("settled retry changed config")
	}
}
