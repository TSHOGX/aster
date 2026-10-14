package product

import (
	"context"
	"testing"
)

func TestEvolutionPrefixReservationsRejectNestedAndAllowDisjoint(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	group := StandardUsersGroup
	first, err := s.ReserveEvolutionPrefix(ctx, token, PrefixReservation{"library", "skills", group, "skills-binding", 1, "", "op-one"}, "op-one")
	must(t, err)
	if first.State != "pending" {
		t.Fatal(first)
	}
	if _, err := s.ReserveEvolutionPrefix(ctx, token, PrefixReservation{"library", "skills/sql", group, "sql-binding", 1, "", "op-two"}, "op-two"); err != ErrConflict {
		t.Fatal("nested prefix won", err)
	}
	second, err := s.ReserveEvolutionPrefix(ctx, token, PrefixReservation{"library", "lessons", group, "lessons-binding", 1, "", "op-three"}, "op-three")
	must(t, err)
	if _, err := s.ActivateEvolutionPrefix(ctx, token, "op-one", "library", "skills", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateEvolutionPrefix(ctx, token, "op-three", "library", "lessons", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEvolutionPrefix(ctx, token, "op-one", "library", "skills"); err != nil {
		t.Fatal(err)
	}
	_ = second
}

func TestEvolutionReservationCannotBeStolenOrRewound(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	r := PrefixReservation{"library", "skills", StandardUsersGroup, "binding", 1, "", "reserve"}
	_, err := s.ReserveEvolutionPrefix(ctx, token, r, r.OperationID)
	must(t, err)
	active, err := s.ActivateEvolutionPrefix(ctx, token, r.OperationID, r.LibraryID, r.Prefix, 1)
	must(t, err)
	again, err := s.ReserveEvolutionPrefix(ctx, token, r, r.OperationID)
	must(t, err)
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
