package authoring

import (
	"context"
	"testing"
)

func TestEvolutionPrefixReservationsRejectNestedAndAllowDisjoint(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	token := testActor
	group := "group"
	first, err := s.ReserveEvolutionPrefix(ctx, token, PrefixReservation{l.ID, "skills", group, "skills-binding", 1, "", "op-one"}, "op-one")
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "pending" {
		t.Fatal(first)
	}
	if _, err := s.ReserveEvolutionPrefix(ctx, token, PrefixReservation{l.ID, "skills/sql", group, "sql-binding", 1, "", "op-two"}, "op-two"); err != ErrConflict {
		t.Fatal("nested prefix won", err)
	}
	second, err := s.ReserveEvolutionPrefix(ctx, token, PrefixReservation{l.ID, "lessons", group, "lessons-binding", 1, "", "op-three"}, "op-three")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateEvolutionPrefix(ctx, token, "op-one", l.ID, "skills", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateEvolutionPrefix(ctx, token, "op-three", l.ID, "lessons", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEvolutionPrefix(ctx, token, "op-one", l.ID, "skills"); err != nil {
		t.Fatal(err)
	}
	_ = second
}

func TestEvolutionReservationCannotBeStolenOrRewound(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	token := testActor
	r := PrefixReservation{l.ID, "skills", "group", "binding", 1, "", "reserve"}
	_, err := s.ReserveEvolutionPrefix(ctx, token, r, r.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.ActivateEvolutionPrefix(ctx, token, r.OperationID, r.LibraryID, r.Prefix, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ReserveEvolutionPrefix(ctx, token, r, r.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != active.State {
		t.Fatal("retry rewound reservation")
	}
	r.BindingID = "other"
	if _, err := s.ReserveEvolutionPrefix(ctx, token, r, r.OperationID); err != ErrConflict {
		t.Fatal("binding stole prefix", err)
	}
	r.Prefix = "../escape"
	if _, err := s.ReserveEvolutionPrefix(ctx, token, r, r.OperationID); err != ErrInvalid {
		t.Fatal("unclean prefix accepted", err)
	}
}
