package authoring

import (
	"context"
	"testing"
)

func TestLibraryEventsHideOtherLibraryActivity(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	s.config.Authority = &branchAuthority{true}
	first := library(t, s)
	_, err := s.Create(ctx, testActor, CreateInput{RequestID: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetMergePolicy(ctx, testActor, first.ID, MergePolicyInput{"policy", 1, 2}); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, testActor, first.ID, 0)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for i, event := range events {
		if event.LibraryID != first.ID || event.Sequence != int64(i+1) {
			t.Fatal("global sequence leaked", events)
		}
	}
	s.config.Authority = &branchAuthority{false}
	if _, err = s.Events(ctx, testActor, first.ID, 1); err != ErrAuthority {
		t.Fatal("revoked continuation allowed", err)
	}
}
