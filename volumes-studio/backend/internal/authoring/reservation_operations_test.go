package authoring

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"testing"
)

func TestEvolutionConfigurationReservationAtomicAndLookupable(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	budget := evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}
	ref := evolution.Revision{ID: "ref", Revision: 1, Digest: evolution.Digest([]byte("ref"))}
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: budget, Sources: []evolution.SourceBinding{{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{"group"}, DerivativePublication: true}}}
	for _, name := range []string{"skills", "lessons"} {
		config.Bindings = append(config.Bindings, evolution.Binding{ID: name, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: l.ID, Prefix: name, Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Budget: budget, Trigger: "manual"})
	}
	_, err := s.ReserveEvolutionPrefix(ctx, testActor, PrefixReservation{LibraryID: l.ID, Prefix: "lessons", GroupID: "group", BindingID: "other", Generation: 1, OperationID: "occupied"}, "occupied")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config)
	input := EvolutionReservationOperation{"operation", evolution.Digest(raw), config}
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, input); err != ErrConflict {
		t.Fatal("conflict accepted", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM evolution_prefix_reservations WHERE prefix='skills'").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial reservation leaked", count, err)
	}
	config.Bindings = config.Bindings[:1]
	raw, _ = json.Marshal(config)
	input.Configuration = config
	input.Digest = evolution.Digest(raw)
	wrong := input
	wrong.Configuration.Bindings = append([]evolution.Binding(nil), input.Configuration.Bindings...)
	wrong.Configuration.Bindings[0].Output.Profile = "shared-assets-v1"
	wrongRaw, _ := json.Marshal(wrong.Configuration)
	wrong.Digest = evolution.Digest(wrongRaw)
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, wrong); err != ErrConflict {
		t.Fatal("wrong library profile accepted", err)
	}
	receipt, err := s.ReserveEvolutionConfiguration(ctx, testActor, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReserveEvolutionConfiguration(ctx, testActor, input)
	if err != nil || replay != receipt {
		t.Fatal("replay mismatch", err)
	}
	lookup, err := s.EvolutionReservationOperation(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || lookup != receipt {
		t.Fatal("lookup mismatch", err)
	}
	s.config.Authority = &branchAuthority{false}
	if _, err = s.EvolutionReservationOperation(ctx, testActor, input.OperationID, input.Digest); err == nil {
		t.Fatal("lookup ignored revoked authority")
	}
}
