package product

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"errors"
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
	desired, err := s.SaveEvolutionConfig(ctx, token, group, EvolutionActivationInput{"config", 0, activationConfigFixture(0+1, true)})
	must(t, err)
	if desired.State != "activating" || desired.ActiveRevision != 0 {
		t.Fatal(desired)
	}
	must(t, s.AcceptEvolutionParticipant(ctx, token, desired.OperationID, "evaluation-space", activationDigestFixture(1, true)))
	if _, err := s.SettleEvolutionActivation(ctx, token, desired.OperationID); err != ErrBusy {
		t.Fatal("one receipt settled", err)
	}
	must(t, s.AcceptEvolutionParticipant(ctx, token, desired.OperationID, "studio", activationDigestFixture(1, true)))
	active, err := s.SettleEvolutionActivation(ctx, token, desired.OperationID)
	must(t, err)
	if active.State != "active" || active.ActiveRevision != 1 || active.ActiveDigest != activationDigestFixture(1, true) {
		t.Fatal(active)
	}
	if _, err := s.SaveEvolutionConfig(ctx, token, group, EvolutionActivationInput{"stale", 0, activationConfigFixture(0+1, false)}); err != ErrConflict {
		t.Fatal("stale desired revision accepted", err)
	}
}

func TestEvolutionActivationAuthorizationReplayAndParticipantIdentity(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input := EvolutionActivationInput{"save-once", 0, activationConfigFixture(0+1, false)}
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
	digest := activationDigestFixture(1, true)
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"first", 0, activationConfigFixture(0+1, true)})
	must(t, err)
	if _, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"second", 1, activationConfigFixture(1+1, false)}); err != ErrBusy {
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
	digest := activationDigestFixture(1, true)
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"save", 0, activationConfigFixture(0+1, true)})
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
	digest := activationDigestFixture(1, true)
	first, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"first", 0, activationConfigFixture(0+1, true)})
	must(t, err)
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, s.AcceptEvolutionParticipant(ctx, token, first.OperationID, participant, digest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, first.OperationID)
	must(t, err)
	nextDigest := activationDigestFixture(2, true)
	next, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"next", 1, activationConfigFixture(1+1, true)})
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
	digest := activationDigestFixture(1, true)
	input := EvolutionActivationInput{"save", 0, activationConfigFixture(0+1, true)}
	pending, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	must(t, s.AcceptEvolutionParticipant(ctx, token, pending.OperationID, "studio", digest))
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evaluation-space", digest, "offline"))
	_, err = s.DisableEvolution(ctx, token, StandardUsersGroup, DisableEvolutionInput{"disable", 1})
	must(t, err)
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	projection, err := s.EvolutionProjection(ctx, token, pending.OperationID)
	must(t, err)
	if projection.Digest != digest || projection.Configuration.Revision != 1 || !projection.Configuration.AutoEnabled {
		t.Fatal("lost immutable projection", projection)
	}
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

func activationConfigFixture(revision int64, enabled bool) evolution.Configuration {
	return evolution.Configuration{GroupID: StandardUsersGroup, Revision: revision, AutoEnabled: enabled, Budget: evolution.Budget{MaxTokens: 100, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1024}}
}
func activationDigestFixture(revision int64, enabled bool) string {
	raw, _ := json.Marshal(activationConfigFixture(revision, enabled))
	return evolution.Digest(raw)
}

func TestEvolutionConfigurationContentAndIdentity(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	input := EvolutionActivationInput{"content", 0, activationConfigFixture(1, true)}
	for _, mutate := range []func(*evolution.Configuration){
		func(c *evolution.Configuration) { c.GroupID = "other" },
		func(c *evolution.Configuration) { c.Revision = 2 },
		func(c *evolution.Configuration) { c.Budget.MaxAttempts = 0 },
	} {
		invalid := input
		mutate(&invalid.Configuration)
		if _, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, invalid); err != ErrInvalid {
			t.Fatalf("invalid configuration accepted: %v", err)
		}
	}
	saved, err := s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, input)
	must(t, err)
	_, err = s.DisableEvolution(ctx, token, StandardUsersGroup, DisableEvolutionInput{"off", 1})
	must(t, err)
	projection, err := s.EvolutionProjection(ctx, token, saved.OperationID)
	must(t, err)
	if !projection.Configuration.AutoEnabled || projection.Digest != saved.DesiredDigest || projection.Configuration.Budget != input.Configuration.Budget {
		t.Fatal("immutable content changed", projection)
	}
	if _, err = s.EvolutionProjection(ctx, "", saved.OperationID); err == nil {
		t.Fatal("unauthorized projection read")
	}
	changed := input
	changed.Configuration.Budget.MaxTokens++
	if _, err = s.SaveEvolutionConfig(ctx, token, StandardUsersGroup, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("request replay replaced content", err)
	}
}
