package product

import (
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
	desired, err := s.SaveEvolutionConfig(ctx, token, group, EvolutionActivationInput{"config", 0, "config-digest", true})
	must(t, err)
	if desired.State != "activating" || desired.ActiveRevision != 0 {
		t.Fatal(desired)
	}
	must(t, s.AcceptEvolutionParticipant(ctx, token, desired.OperationID, "evaluation-space", "config-digest"))
	if _, err := s.SettleEvolutionActivation(ctx, token, desired.OperationID); err != ErrBusy {
		t.Fatal("one receipt settled", err)
	}
	must(t, s.AcceptEvolutionParticipant(ctx, token, desired.OperationID, "studio", "config-digest"))
	active, err := s.SettleEvolutionActivation(ctx, token, desired.OperationID)
	must(t, err)
	if active.State != "active" || active.ActiveRevision != 1 || active.ActiveDigest != "config-digest" {
		t.Fatal(active)
	}
	if _, err := s.SaveEvolutionConfig(ctx, token, group, EvolutionActivationInput{"stale", 0, "next", false}); err != ErrConflict {
		t.Fatal("stale desired revision accepted", err)
	}
}

func TestEvolutionActivationAuthorizationReplayAndParticipantIdentity(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input := EvolutionActivationInput{"save-once", 0, "digest", false}
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
	if err = s.AcceptEvolutionParticipant(ctx, token, first.OperationID, "invented", "digest"); err != ErrInvalid {
		t.Fatal("unknown participant accepted", err)
	}
	if _, err = s.SettleEvolutionActivation(ctx, token, first.OperationID); err != ErrBusy {
		t.Fatal("missing required receipts settled", err)
	}
}
