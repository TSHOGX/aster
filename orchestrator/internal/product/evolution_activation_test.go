package product

import (
	"aster/contracts/evolution"
	"context"
	"path/filepath"
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

func TestEvolutionDisableDuringPendingActivation(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	digest := evolution.Digest([]byte("config"))
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"save", 0, digest, true})
	must(t, err)
	input := DisableEvolutionInput{"disable", 1}
	disabled, err := s.DisableEvolution(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if disabled.AutoEnabled || disabled.OperationID != pending.OperationID || disabled.State != "activating" {
		t.Fatal("disable altered activation identity", disabled)
	}
	again, err := s.DisableEvolution(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if again != disabled {
		t.Fatal("disable retry changed result")
	}
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, s.AcceptEvolutionParticipant(ctx, token, pending.OperationID, participant, digest))
	}
	active, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	if active.AutoEnabled || active.ActiveRevision != 1 {
		t.Fatal("late activation reenabled automatic work", active)
	}
}

func TestEvolutionFailureRetryRetainsAcceptedConfiguration(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	digest := evolution.Digest([]byte("first"))
	first, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"first", 0, digest, true})
	must(t, err)
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, s.AcceptEvolutionParticipant(ctx, token, first.OperationID, participant, digest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, first.OperationID)
	must(t, err)
	nextDigest := evolution.Digest([]byte("next"))
	next, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"next", 1, nextDigest, true})
	must(t, err)
	must(t, s.AcceptEvolutionParticipant(ctx, token, next.OperationID, "studio", nextDigest))
	must(t, s.FailEvolutionParticipant(ctx, token, next.OperationID, "evaluation-space", nextDigest, "offline"))
	failed, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if failed.ActiveRevision != 1 || failed.ActiveDigest != digest || failed.State != "failed" {
		t.Fatal(failed)
	}
	if err = s.AcceptEvolutionParticipant(ctx, token, next.OperationID, "evaluation-space", nextDigest); err != ErrConflict {
		t.Fatal("failure bypassed explicit retry", err)
	}
	must(t, s.RetryEvolutionActivation(ctx, token, next.OperationID))
	must(t, s.AcceptEvolutionParticipant(ctx, token, next.OperationID, "evaluation-space", nextDigest))
	settled, err := s.SettleEvolutionActivation(ctx, token, next.OperationID)
	must(t, err)
	if settled.ActiveRevision != 2 || settled.OperationID != next.OperationID {
		t.Fatal(settled)
	}
}

func TestEvolutionActivationRecoveryAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "evolution.db")
	s, err := Open(ctx, path)
	must(t, err)
	defer func() { s.Close() }()
	admin, err := s.BootstrapAccount(ctx, "admin", "permanent-admin-password", bootstrapSeedFixture(t))
	must(t, err)
	token := adminTestToken(t, s, admin)
	digest := evolution.Digest([]byte("retained"))
	input := EvolutionActivationInput{"save", 0, digest, true}
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	must(t, s.AcceptEvolutionParticipant(ctx, token, pending.OperationID, "studio", digest))
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evaluation-space", digest, "offline"))
	_, err = s.DisableEvolution(ctx, token, StandardUsersGroup, DisableEvolutionInput{"disable", 1})
	must(t, err)
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	current, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if current.State != "failed" || current.AutoEnabled || current.OperationID != pending.OperationID {
		t.Fatal("lost activation state", current)
	}
	replay, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if replay.OperationID != pending.OperationID {
		t.Fatal("request replay admitted new operation")
	}
	must(t, s.RetryEvolutionActivation(ctx, token, pending.OperationID))
	must(t, s.AcceptEvolutionParticipant(ctx, token, pending.OperationID, "evaluation-space", digest))
	settled, err := s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	if settled.AutoEnabled || settled.ActiveRevision != 1 {
		t.Fatal(settled)
	}
}
