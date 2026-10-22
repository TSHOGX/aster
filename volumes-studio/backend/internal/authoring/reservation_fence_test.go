package authoring

import (
	"aster/contracts/evolution"
	"context"
	"testing"
)

func TestEvolutionOutputRequiresCurrentActiveOwnership(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	output := evolution.OutputBinding{LibraryID: l.ID, Prefix: "skills", Profile: l.Profile, ReservationGeneration: 1}
	r := PrefixReservation{LibraryID: l.ID, Prefix: "skills", GroupID: "group", BindingID: "binding", Generation: 1, OperationID: "operation"}
	if _, err := s.ReserveEvolutionPrefix(ctx, testActor, r, r.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEvolutionOutput(ctx, testActor, "group", "binding", output); err != ErrConflict {
		t.Fatal("pending reservation authorized", err)
	}
	if _, err := s.ActivateEvolutionPrefix(ctx, testActor, r.OperationID, l.ID, r.Prefix, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEvolutionOutput(ctx, testActor, "group", "binding", output); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*evolution.OutputBinding){func(o *evolution.OutputBinding) { o.ReservationGeneration = 2 }, func(o *evolution.OutputBinding) { o.Prefix = "skills/nested" }, func(o *evolution.OutputBinding) { o.Profile = "shared-assets-v1" }} {
		changed := output
		change(&changed)
		if err := s.CheckEvolutionOutput(ctx, testActor, "group", "binding", changed); err != ErrConflict {
			t.Fatal("changed output authorized", changed, err)
		}
	}
	if err := s.CheckEvolutionOutput(ctx, testActor, "other", "binding", output); err != ErrConflict {
		t.Fatal("other group authorized", err)
	}
	if err := s.CheckEvolutionOutput(ctx, testActor, "group", "other", output); err != ErrConflict {
		t.Fatal("other binding authorized", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE libraries SET archived=1 WHERE id=?", l.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEvolutionOutput(ctx, testActor, "group", "binding", output); err != ErrConflict {
		t.Fatal("archived output authorized", err)
	}
}
