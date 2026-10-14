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
	for _, statement := range []string{
		"UPDATE evolution_reservation_operations SET body='{}' WHERE operation_id='operation'",
		"UPDATE evolution_reservation_operations SET digest='changed' WHERE operation_id='operation'",
		"UPDATE evolution_reservation_operations SET group_id='other' WHERE operation_id='operation'",
		"DELETE FROM evolution_reservation_operations WHERE operation_id='operation'",
	} {
		if _, err = s.db.ExecContext(ctx, statement); err == nil {
			t.Fatal("reservation provenance mutation accepted", statement)
		}
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
	s.config.Authority = &branchAuthority{true}
	if _, err = s.ActivateEvolutionPrefix(ctx, testActor, input.OperationID, l.ID, "skills", 1); err != ErrConflict {
		t.Fatal("partial batch activation allowed", err)
	}
	if err = s.ReleaseEvolutionPrefix(ctx, testActor, input.OperationID, l.ID, "skills"); err != ErrConflict {
		t.Fatal("partial batch release allowed", err)
	}
	adopted, err := s.AdoptEvolutionConfiguration(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || adopted.State != "adopted" {
		t.Fatal(adopted, err)
	}
	replay, err = s.AdoptEvolutionConfiguration(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || replay != adopted {
		t.Fatal("adoption replay", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE evolution_reservation_operations SET state='reserved' WHERE operation_id=?", input.OperationID); err == nil {
		t.Fatal("terminal operation rewound")
	}
	if _, err = s.CancelEvolutionReservation(ctx, testActor, input.OperationID, input.Digest); err != ErrConflict {
		t.Fatal("adopted ownership released", err)
	}
	update := input
	update.Configuration.Bindings = append([]evolution.Binding(nil), input.Configuration.Bindings...)
	update.Configuration.Bindings[0].Output.ReservationGeneration = 2
	update.OperationID = "update-active"
	updateRaw, _ := json.Marshal(update.Configuration)
	update.Digest = evolution.Digest(updateRaw)
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, update); err != nil {
		t.Fatal("active candidate reservation", err)
	}
	assertCurrent := func(operation string, generation int64) {
		t.Helper()
		var current string
		var gen int64
		if err := s.db.QueryRowContext(ctx, "SELECT operation_id,generation FROM evolution_prefix_reservations WHERE library_id=? AND prefix='skills'", l.ID).Scan(&current, &gen); err != nil || current != operation || gen != generation {
			t.Fatal("active generation changed", current, gen, err)
		}
	}
	assertCurrent(input.OperationID, 1)
	oldOutput := input.Configuration.Bindings[0].Output
	newOutput := update.Configuration.Bindings[0].Output
	if err = s.CheckEvolutionOutput(ctx, testActor, "group", input.Configuration.Bindings[0].ID, oldOutput); err != nil {
		t.Fatal("candidate revoked active generation", err)
	}
	if err = s.CheckEvolutionOutput(ctx, testActor, "group", input.Configuration.Bindings[0].ID, newOutput); err != ErrConflict {
		t.Fatal("pending candidate authorized effects", err)
	}

	contender := update
	contender.OperationID = "competing-update"
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, contender); err != ErrConflict {
		t.Fatal("concurrent candidate won", err)
	}
	if _, err = s.CancelEvolutionReservation(ctx, testActor, update.OperationID, update.Digest); err != nil {
		t.Fatal(err)
	}
	assertCurrent(input.OperationID, 1)
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, contender); err != nil {
		t.Fatal("candidate after cancellation", err)
	}
	if _, err = s.AdoptEvolutionConfiguration(ctx, testActor, contender.OperationID, contender.Digest); err != nil {
		t.Fatal(err)
	}
	assertCurrent(contender.OperationID, 2)
	if err = s.CheckEvolutionOutput(ctx, testActor, "group", input.Configuration.Bindings[0].ID, oldOutput); err != ErrConflict {
		t.Fatal("stale run generation retained authority", err)
	}
	if err = s.CheckEvolutionOutput(ctx, testActor, "group", input.Configuration.Bindings[0].ID, newOutput); err != nil {
		t.Fatal("adopted generation not authorized", err)
	}
	historical, err := s.EvolutionReservationOperation(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || historical.State != "adopted" {
		t.Fatal("historical receipt lost", historical, err)
	}

	if _, err = s.CancelEvolutionReservation(ctx, testActor, update.OperationID, update.Digest); err != nil {
		t.Fatal("cancelled candidate replay", err)
	}
	assertCurrent(contender.OperationID, 2)
	input.OperationID = "cancel-operation"
	input.Configuration.Bindings[0].ID = "draft-binding"
	input.Configuration.Bindings[0].Output.Prefix = "draft"
	raw, _ = json.Marshal(input.Configuration)
	input.Digest = evolution.Digest(raw)
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, input); err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelEvolutionReservation(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || cancelled.State != "released" {
		t.Fatal(cancelled, err)
	}
	replay, err = s.CancelEvolutionReservation(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || replay != cancelled {
		t.Fatal("cancellation replay", err)
	}
	if _, err = s.AdoptEvolutionConfiguration(ctx, testActor, input.OperationID, input.Digest); err != ErrConflict {
		t.Fatal("cancelled operation resurrected", err)
	}

	next := input
	next.OperationID = "replacement"
	next.Configuration.Bindings = append([]evolution.Binding(nil), input.Configuration.Bindings...)
	next.Configuration.Bindings[0].Output.ReservationGeneration = 2
	nextRaw, _ := json.Marshal(next.Configuration)
	next.Digest = evolution.Digest(nextRaw)
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, next); err != nil {
		t.Fatal("cancelled prefix could not be reserved again", err)
	}
	oldReceipt, err := s.CancelEvolutionReservation(ctx, testActor, input.OperationID, input.Digest)
	if err != nil || oldReceipt.State != "released" {
		t.Fatal("old cancellation retry", err)
	}
	currentReceipt, err := s.EvolutionReservationOperation(ctx, testActor, next.OperationID, next.Digest)
	if err != nil || currentReceipt.State != "reserved" {
		t.Fatal("old cancellation affected replacement", err)
	}
	if _, err = s.AdoptEvolutionConfiguration(ctx, testActor, next.OperationID, next.Digest); err != nil {
		t.Fatal("replacement adoption", err)
	}
	second, err := s.Create(ctx, testActor, CreateInput{RequestID: "second-library", Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	firstBinding := input.Configuration.Bindings[0]
	firstBinding.ID = "first-batch"
	firstBinding.Output.Prefix = "atomic"
	secondBinding := firstBinding
	secondBinding.ID = "second-batch"
	secondBinding.Output.LibraryID = second.ID
	input.Configuration.Bindings = []evolution.Binding{firstBinding, secondBinding}
	input.OperationID = "atomic-adopt"
	raw, _ = json.Marshal(input.Configuration)
	input.Digest = evolution.Digest(raw)
	if _, err = s.ReserveEvolutionConfiguration(ctx, testActor, input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE libraries SET archived=1 WHERE id=?", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdoptEvolutionConfiguration(ctx, testActor, input.OperationID, input.Digest); err != ErrConflict {
		t.Fatal("archived output adopted", err)
	}
	var state string
	if err = s.db.QueryRowContext(ctx, "SELECT state FROM evolution_prefix_reservations WHERE operation_id=? AND library_id=?", input.OperationID, l.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatal("partial adoption escaped rollback", state, err)
	}
	if _, err = s.CancelEvolutionReservation(ctx, testActor, input.OperationID, input.Digest); err != nil {
		t.Fatal("archived batch cleanup failed", err)
	}

}

func TestReservationLookupRejectsStoredScopeSubstitution(t *testing.T) {
	s, _ := testStore(t)
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	input := EvolutionReservationOperation{OperationID: "operation", Digest: evolution.Digest(raw), Configuration: config}
	_, err := s.ReserveEvolutionConfiguration(context.Background(), testActor, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		group    string
		revision int64
	}{{"other", 1}, {"group", 2}} {
		if _, err = s.ScopedEvolutionReservation(context.Background(), testActor, scope.group, input.OperationID, input.Digest, scope.revision); err != ErrConflict {
			t.Fatal("scope substitution", scope, err)
		}
	}
	if _, err = s.ScopedEvolutionReservation(context.Background(), testActor, "group", input.OperationID, input.Digest, 1); err != nil {
		t.Fatal(err)
	}
}
