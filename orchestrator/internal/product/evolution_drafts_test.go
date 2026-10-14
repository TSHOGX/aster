package product

import (
	"aster/contracts/evolution"
	"context"
	"errors"
	"testing"
)

func TestEvolutionDraftIncompleteReplayAndIsolation(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	active, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	input := EvolutionDraftInput{"draft", 0, evolution.Configuration{GroupID: StandardUsersGroup}}
	first, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input)
	must(t, err)
	again, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if first.Revision != 1 || again.Revision != 1 {
		t.Fatal("replay advanced draft")
	}
	read, err := s.EvolutionDraft(ctx, token, StandardUsersGroup)
	must(t, err)
	if read.Configuration.Validate() == nil {
		t.Fatal("fixture should remain incomplete")
	}
	current, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	if current != active {
		t.Fatal("draft changed activation")
	}
	input.RequestID = "stale"
	if _, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input); !errors.Is(err, ErrConflict) {
		t.Fatal("stale overwrite", err)
	}
	input.ExpectedRevision = 1
	input.Configuration.Budget = activationConfigFixture(1, true).Budget
	second, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input)
	must(t, err)
	if second.Revision != 2 {
		t.Fatal(second)
	}
	if _, err = s.EvolutionDraft(ctx, "", StandardUsersGroup); err == nil {
		t.Fatal("anonymous draft read")
	}
	input.RequestID = "enabled"
	input.ExpectedRevision = 2
	input.Configuration.AutoEnabled = true
	if _, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, input); err != ErrInvalid {
		t.Fatal("enabled draft", err)
	}
}

func TestEvolutionDraftActivationAndReplayAfterEdit(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	config := evolution.Configuration{GroupID: StandardUsersGroup}
	_, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{"draft", 0, config})
	must(t, err)
	request := EvolutionDraftActivationInput{"activate", 0, 1, true}
	if _, err = s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, request); err != ErrInvalid {
		t.Fatal("incomplete activation", err)
	}
	config.Budget = activationConfigFixture(1, false).Budget
	_, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{"complete", 1, config})
	must(t, err)
	request.ExpectedDraftRevision = 2
	active, err := s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	config.Budget.MaxTokens++
	_, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{"edit", 2, config})
	must(t, err)
	replay, err := s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	if replay != active {
		t.Fatal("replay changed activation")
	}
	request.RequestID = "stale"
	if _, err = s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, request); err != ErrConflict {
		t.Fatal("stale draft promoted", err)
	}
	projection, err := s.EvolutionProjection(ctx, token, active.OperationID)
	must(t, err)
	if projection.Configuration.Budget.MaxTokens != 100 || !projection.Configuration.AutoEnabled {
		t.Fatal("draft edit changed projection")
	}
}
