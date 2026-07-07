package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

func TestAuthSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := AuthSession{
		TokenHash: "hash-1",
		UserID:    "lab",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := st.CreateAuthSession(ctx, session); err != nil {
		t.Fatalf("create: %v", err)
	}

	userID, err := st.LookupAuthSession(ctx, "hash-1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if userID != "lab" {
		t.Fatalf("want lab, got %q", userID)
	}

	// Expired.
	if _, err := st.LookupAuthSession(ctx, "hash-1", now.Add(2*time.Hour)); !errors.Is(err, ErrAuthSessionInvalid) {
		t.Fatalf("expected ErrAuthSessionInvalid for expired, got %v", err)
	}

	// Unknown token.
	if _, err := st.LookupAuthSession(ctx, "nope", now); !errors.Is(err, ErrAuthSessionInvalid) {
		t.Fatalf("expected ErrAuthSessionInvalid for unknown, got %v", err)
	}
}

func TestAuthSessionRevoke(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	for _, hash := range []string{"a", "b"} {
		if err := st.CreateAuthSession(ctx, AuthSession{TokenHash: hash, UserID: "lab", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("create %s: %v", hash, err)
		}
	}

	if err := st.RevokeAuthSession(ctx, "a", now); err != nil {
		t.Fatalf("revoke a: %v", err)
	}
	if _, err := st.LookupAuthSession(ctx, "a", now); !errors.Is(err, ErrAuthSessionInvalid) {
		t.Fatalf("revoked token should be invalid, got %v", err)
	}
	if _, err := st.LookupAuthSession(ctx, "b", now); err != nil {
		t.Fatalf("b should still be valid: %v", err)
	}

	if err := st.RevokeAuthSessionsForUser(ctx, "lab", now); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if _, err := st.LookupAuthSession(ctx, "b", now); !errors.Is(err, ErrAuthSessionInvalid) {
		t.Fatalf("b should be revoked after revoke-all, got %v", err)
	}
}

func TestSweepExpiredAuthSessions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	_ = st.CreateAuthSession(ctx, AuthSession{TokenHash: "live", UserID: "lab", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	_ = st.CreateAuthSession(ctx, AuthSession{TokenHash: "dead", UserID: "lab", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)})

	removed, err := st.SweepExpiredAuthSessions(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Fatalf("want 1 removed, got %d", removed)
	}
	if _, err := st.LookupAuthSession(ctx, "live", now); err != nil {
		t.Fatalf("live should survive sweep: %v", err)
	}
}

func TestListSessionsByUser(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	mk := func(id, user string) Session {
		return Session{
			ID: id, UserID: user, Status: string(sessionstate.Created),
			Agent: "pi", Mode: "agent", Workspace: dir, RestoreID: "r-" + id,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	for _, s := range []Session{mk("s1", "lab"), mk("s2", "alice"), mk("s3", "lab")} {
		if err := st.CreateSession(ctx, s); err != nil {
			t.Fatalf("create %s: %v", s.ID, err)
		}
	}

	labSessions, err := st.ListSessionsByUser(ctx, "lab")
	if err != nil {
		t.Fatalf("list lab: %v", err)
	}
	if len(labSessions) != 2 {
		t.Fatalf("want 2 lab sessions, got %d", len(labSessions))
	}
	for _, s := range labSessions {
		if s.UserID != "lab" {
			t.Fatalf("unexpected owner %q in lab list", s.UserID)
		}
	}

	aliceSessions, err := st.ListSessionsByUser(ctx, "alice")
	if err != nil {
		t.Fatalf("list alice: %v", err)
	}
	if len(aliceSessions) != 1 || aliceSessions[0].ID != "s2" {
		t.Fatalf("alice list mismatch: %+v", aliceSessions)
	}
}
