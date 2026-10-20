package authoring

import (
	"context"
	"errors"
	"testing"

	"aster/contracts/evolution"
)

type ownerRecorder struct {
	claims []string
	fail   error
}

func (o *ownerRecorder) ClaimLibrary(_ context.Context, principal evolution.Principal, library string) error {
	if o.fail != nil {
		return o.fail
	}
	o.claims = append(o.claims, principal.Subject+":"+library)
	return nil
}

func TestCreateClaimsOwnershipBeforeCommitAndReplaysWithoutReclaiming(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	owners := &ownerRecorder{fail: ErrAuthority}
	s.config.Owners = owners
	if _, err := s.Create(ctx, testActor, CreateInput{RequestID: "refused", Name: "Refused"}); !errors.Is(err, ErrAuthority) {
		t.Fatal("refused claim still created a library", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM libraries WHERE name='Refused'").Scan(&count); err != nil || count != 0 {
		t.Fatal("ownerless library committed", count, err)
	}
	owners.fail = nil
	first, err := s.Create(ctx, testActor, CreateInput{RequestID: "owned", Name: "Owned"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Create(ctx, testActor, CreateInput{RequestID: "owned", Name: "Owned"})
	if err != nil || again.ID != first.ID {
		t.Fatal("create replay changed", err)
	}
	if len(owners.claims) != 1 || owners.claims[0] != testActor.Subject+":"+first.ID {
		t.Fatal("owner claim", owners.claims)
	}
}
