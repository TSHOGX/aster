package authoring

import (
	"context"
	"fmt"
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
	if err := s.ReleaseEvolutionPrefix(ctx, token, "op-one", l.ID, "skills"); err != ErrConflict {
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

func TestReservationCompensationOnlyOwnsPendingOperation(t *testing.T) {
	s, _ := testStore(t)
	l := library(t, s)
	s.config.Authority = &branchAuthority{true}
	ctx := context.Background()
	r := PrefixReservation{l.ID, "skills", "group", "binding", 1, "", "owned"}
	if _, err := s.ReserveEvolutionPrefix(ctx, testActor, r, r.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEvolutionPrefix(ctx, testActor, "other", l.ID, r.Prefix); err == nil {
		t.Fatal("foreign compensation accepted")
	}
	if _, err := s.ActivateEvolutionPrefix(ctx, testActor, r.OperationID, l.ID, r.Prefix, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEvolutionPrefix(ctx, testActor, r.OperationID, l.ID, r.Prefix); err != ErrConflict {
		t.Fatal("active ownership released by compensation", err)
	}
	r.Prefix = "lessons"
	r.BindingID = "lessons"
	r.OperationID = "pending"
	if _, err := s.ReserveEvolutionPrefix(ctx, testActor, r, r.OperationID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.ReleaseEvolutionPrefix(ctx, testActor, r.OperationID, l.ID, r.Prefix); err != nil {
			t.Fatal("pending compensation failed", err)
		}
	}
}

func TestConcurrentPrefixReservations(t *testing.T) {
	for _, scenario := range []struct {
		name, left, right, secondGroup string
		winners                        int
	}{{"nested", "skills", "skills/sql", "alpha", 1}, {"disjoint", "skills", "lessons", "alpha", 2}, {"different-owner", "skills", "lessons", "beta", 1}} {
		t.Run(scenario.name, func(t *testing.T) {
			s, _ := testStore(t)
			l := library(t, s)
			s.config.Authority = &branchAuthority{true}
			ctx := context.Background()
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, prefix := range []string{scenario.left, scenario.right} {
				go func(i int, prefix string) {
					<-start
					group := "alpha"
					if i == 1 {
						group = scenario.secondGroup
					}
					operation := fmt.Sprintf("operation-%d", i)
					_, err := s.ReserveEvolutionPrefix(ctx, testActor, PrefixReservation{LibraryID: l.ID, Prefix: prefix, GroupID: group, BindingID: operation, Generation: 1, OperationID: operation}, operation)
					results <- err
				}(i, prefix)
			}
			close(start)
			winners := 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					winners++
				} else if err != ErrConflict {
					t.Fatal(err)
				}
			}
			if winners != scenario.winners {
				t.Fatalf("got %d winners, expected %d", winners, scenario.winners)
			}
		})
	}
}
