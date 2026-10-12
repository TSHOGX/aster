package product

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestIdentityInvalidationFollowsCommitAndSkipsFailedMutations(t *testing.T) {
	ctx := context.Background()
	s, _ := accountStore(t)
	changes := s.IdentityInvalidations()
	if err := s.writeIdentity(ctx, func(*sql.Tx) error { return ErrConflict }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("failed transaction invalidated authority")
	default:
	}
	if err := s.RevokeLogin(ctx, "missing-cookie"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("anonymous logout invalidated other users")
	default:
	}
	_, token, err := s.Authenticate(ctx, "admin", "permanent-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
		t.Fatal("new login invalidated other sessions")
	default:
	}
	if err = s.RevokeAllLogins(ctx, token); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	default:
		t.Fatal("committed revocation did not notify")
	}
	if _, err = s.Login(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("notification preceded committed revocation", err)
	}
	next := s.IdentityInvalidations()
	if changes == next {
		t.Fatal("invalidation generation reused")
	}
	select {
	case <-next:
		t.Fatal("new subscription is already invalidated")
	default:
	}
}
