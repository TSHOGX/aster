package product

import (
	"aster/contracts/evolution"
	"database/sql"
	"testing"
)

func TestEvolutionConfigurationAndDraftProvenanceImmutable(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	draft := EvolutionDraftInput{RequestID: "draft", ExpectedRevision: 0, Configuration: evolution.Configuration{GroupID: StandardUsersGroup}}
	_, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, draft)
	must(t, err)
	config := EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)}
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, config)
	must(t, err)
	for _, statement := range []string{
		"UPDATE evolution_configuration_revisions SET body='{}'",
		"UPDATE evolution_configuration_revisions SET actor_id='other'",
		"DELETE FROM evolution_configuration_revisions",
		"UPDATE evolution_configuration_drafts SET body='{}'",
		"UPDATE evolution_configuration_drafts SET revision=2",
		"DELETE FROM evolution_configuration_drafts",
	} {
		if err = s.write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, statement); return err }); err == nil {
			t.Fatal("provenance mutation accepted", statement)
		}
	}
	replay, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, config)
	must(t, err)
	if replay != pending {
		t.Fatal("activation replay changed after rejected writes")
	}
	again, err := s.SaveEvolutionDraft(ctx, token, StandardUsersGroup, draft)
	must(t, err)
	if again.Revision != 1 {
		t.Fatal(again)
	}
	projection, err := s.EvolutionProjection(ctx, token, pending.OperationID)
	must(t, err)
	if projection.Digest != pending.DesiredDigest {
		t.Fatal(projection)
	}
}
