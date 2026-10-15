package product

import (
	"aster/contracts/evolution"
	"testing"
)

func TestEvolutionPreviewDoesNotActivateAndPinsDraft(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	config := evolution.Configuration{GroupID: StandardUsersGroup}
	_, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{RequestID: "save", ExpectedRevision: 0, Configuration: config})
	must(t, err)
	request := EvolutionPreviewInput{ExpectedDraftRevision: 1, AutoEnabled: true}
	preview, err := s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	if preview.CentralValid || len(preview.Issues) != 1 || preview.Issues[0] != "configuration_incomplete" {
		t.Fatal(preview)
	}
	config.Budget = activationConfigFixture(1, false).Budget
	_, err = s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, EvolutionDraftInput{RequestID: "complete", ExpectedRevision: 1, Configuration: config})
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
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evaluation-space", pending.DesiredDigest, "catalog_rejected"))
	must(t, s.DiscardEvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID, EvolutionRetryInput{"discard", 1}))
	preview, err = s.PreviewEvolutionDraft(ctx, token, StandardUsersGroup, request)
	must(t, err)
	if preview.CentralValid || len(preview.Issues) != 1 || preview.Issues[0] != "activation_pending" {
		t.Fatal("preview allowed replacement during cleanup", preview)
	}

}

func TestEvolutionPreviewReportsExactBindingChangesAndRejectsBadSchedule(t *testing.T) {
	s, _, _, _, token, source := corpusFixture(t)
	activateCorpusCollector(t, s, token, source, 1, false)
	current, err := s.EvolutionConfig(ctx, token, "fixture-users")
	must(t, err)
	p, err := s.EvolutionProjection(ctx, token, current.OperationID)
	must(t, err)
	config := p.Configuration
	config.Revision = 0
	config.AutoEnabled = false
	config.Bindings[0].Output.Prefix = "revised"
	_, err = s.SaveEvolutionDraft(ctx, token, "fixture-users", EvolutionDraftInput{RequestID: "save", Configuration: config})
	must(t, err)
	preview, err := s.PreviewEvolutionDraft(ctx, token, "fixture-users", EvolutionPreviewInput{ExpectedRevision: 1, ExpectedDraftRevision: 1})
	must(t, err)
	if !preview.CentralValid || preview.ActiveRevision != 1 || len(preview.Changes) != 1 || preview.Changes[0].Kind != "changed" || preview.Changes[0].Before.Output.Prefix != "skills" || preview.Changes[0].After.Output.Prefix != "revised" {
		t.Fatal(preview)
	}
	config.Bindings[0].Trigger = "schedule"
	config.Bindings[0].Schedule = "bad recurrence"
	config.Bindings[0].Timezone = "UTC"
	_, err = s.SaveEvolutionDraft(ctx, token, "fixture-users", EvolutionDraftInput{RequestID: "bad-schedule", ExpectedRevision: 1, Configuration: config})
	must(t, err)
	preview, err = s.PreviewEvolutionDraft(ctx, token, "fixture-users", EvolutionPreviewInput{ExpectedRevision: 1, ExpectedDraftRevision: 2})
	must(t, err)
	if preview.CentralValid || len(preview.Issues) != 1 || preview.Issues[0] != "invalid_schedule" {
		t.Fatal("invalid recurrence previewed valid", preview)
	}
}
