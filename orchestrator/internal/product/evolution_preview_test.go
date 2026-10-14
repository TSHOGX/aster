package product

import (
	"aster/contracts/evolution"
	"testing"
)

func TestEvolutionPreviewDoesNotActivateAndPinsDraft(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	config := evolution.Configuration{GroupID: StandardUsersGroup}
	_, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{"save", 0, config})
	must(t, err)
	request := EvolutionPreviewInput{ExpectedDraftRevision: 1, AutoEnabled: true}
	preview, err := s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	if preview.CentralValid || len(preview.Issues) != 1 || preview.Issues[0] != "configuration_incomplete" {
		t.Fatal(preview)
	}
	config.Budget = activationConfigFixture(1, false).Budget
	_, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{"complete", 1, config})
	must(t, err)
	if _, err = s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, request); err != ErrConflict {
		t.Fatal("stale preview", err)
	}
	request.ExpectedDraftRevision = 2
	preview, err = s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	if !preview.CentralValid || preview.Digest != activationDigestFixture(1, true) || len(preview.RequiredParticipants) != 2 {
		t.Fatal(preview)
	}
	var count int
	must(t, s.reader.QueryRowContext(ctx, "SELECT count(*) FROM evolution_group_configs").Scan(&count))
	if count != 0 {
		t.Fatal("preview created activation")
	}
	pending, err := s.ActivateEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftActivationInput{"activate", 0, 2, true})
	must(t, err)
	if pending.DesiredDigest != preview.Digest {
		t.Fatal("preview digest mismatch")
	}
	request.ExpectedRevision = 1
	preview, err = s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	if preview.CentralValid || preview.Issues[0] != "activation_pending" {
		t.Fatal(preview)
	}
}
