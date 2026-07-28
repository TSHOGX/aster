package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newShareTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "shares.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedShareSession(t *testing.T, st *Store, id, owner string) Session {
	t.Helper()
	now := time.Now().UTC()
	session := Session{
		ID:        id,
		UserID:    owner,
		Status:    "created",
		Agent:     "pi",
		Mode:      "agent",
		Workspace: "/tmp/" + id,
		RestoreID: "unused-" + id,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	return session
}

func newTestShare(id, sessionID, grantee, owner, visibility string) SessionShare {
	return SessionShare{
		ID:            id,
		SessionID:     sessionID,
		GranteeUserID: grantee,
		Role:          ShareRoleReader,
		Visibility:    visibility,
		CreatedBy:     owner,
		CreatedAt:     time.Now().UTC(),
	}
}

func TestSessionShareResolveAndRevoke(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")

	if err := st.CreateSessionShare(ctx, newTestShare("share_1", "sess_1", "bob", "alice", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share: %v", err)
	}

	share, ok, err := st.ResolveSessionShare(ctx, "sess_1", "bob")
	if err != nil || !ok {
		t.Fatalf("resolve share: ok=%v err=%v", ok, err)
	}
	if share.Visibility != ShareVisibilityFull || share.Role != ShareRoleReader {
		t.Fatalf("unexpected share terms: %+v", share)
	}

	if _, ok, err := st.ResolveSessionShare(ctx, "sess_1", "carol"); err != nil || ok {
		t.Fatalf("expected no share for carol: ok=%v err=%v", ok, err)
	}

	if err := st.RevokeSessionShare(ctx, RevokeShareParams{SessionID: "sess_1", ID: "share_1"}); err != nil {
		t.Fatalf("revoke share: %v", err)
	}
	if _, ok, err := st.ResolveSessionShare(ctx, "sess_1", "bob"); err != nil || ok {
		t.Fatalf("expected revoked share to stop resolving: ok=%v err=%v", ok, err)
	}
	shares, err := st.ListSessionShares(ctx, "sess_1")
	if err != nil {
		t.Fatalf("list shares: %v", err)
	}
	if len(shares) != 0 {
		t.Fatalf("expected revoked share excluded from listing, got %d", len(shares))
	}
}

// A revoked grant must not block re-sharing with the same person: the unique
// index is partial on revoked_at IS NULL precisely so this works.
func TestSessionShareRevokeThenReshareSucceeds(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")

	if err := st.CreateSessionShare(ctx, newTestShare("share_1", "sess_1", "bob", "alice", ShareVisibilityFull)); err != nil {
		t.Fatalf("create first share: %v", err)
	}
	if err := st.CreateSessionShare(ctx, newTestShare("share_dup", "sess_1", "bob", "alice", ShareVisibilityFull)); err == nil {
		t.Fatal("expected duplicate live share to be rejected")
	}
	if err := st.RevokeSessionShare(ctx, RevokeShareParams{SessionID: "sess_1", ID: "share_1"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := st.CreateSessionShare(ctx, newTestShare("share_2", "sess_1", "bob", "alice", ShareVisibilitySummary)); err != nil {
		t.Fatalf("expected re-share after revoke to succeed, got %v", err)
	}
	share, ok, err := st.ResolveSessionShare(ctx, "sess_1", "bob")
	if err != nil || !ok {
		t.Fatalf("resolve re-share: ok=%v err=%v", ok, err)
	}
	if share.ID != "share_2" || share.Visibility != ShareVisibilitySummary {
		t.Fatalf("unexpected re-share: %+v", share)
	}
}

// A revoke must not cross session boundaries even when the id is known.
func TestRevokeSessionShareRejectsCrossSessionID(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	seedShareSession(t, st, "sess_2", "dave")

	if err := st.CreateSessionShare(ctx, newTestShare("share_1", "sess_1", "bob", "alice", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share: %v", err)
	}
	err := st.RevokeSessionShare(ctx, RevokeShareParams{SessionID: "sess_2", ID: "share_1"})
	if !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("expected ErrShareNotFound for cross-session revoke, got %v", err)
	}
	if _, ok, err := st.ResolveSessionShare(ctx, "sess_1", "bob"); err != nil || !ok {
		t.Fatalf("cross-session revoke must not touch the real share: ok=%v err=%v", ok, err)
	}
}

func TestListSessionsSharedWith(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	seedShareSession(t, st, "sess_2", "alice")
	seedShareSession(t, st, "sess_3", "alice")

	if err := st.CreateSessionShare(ctx, newTestShare("share_1", "sess_1", "bob", "alice", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share 1: %v", err)
	}
	if err := st.CreateSessionShare(ctx, newTestShare("share_2", "sess_2", "bob", "alice", ShareVisibilitySummary)); err != nil {
		t.Fatalf("create share 2: %v", err)
	}
	if err := st.CreateSessionShare(ctx, newTestShare("share_3", "sess_3", "carol", "alice", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share 3: %v", err)
	}

	shared, err := st.ListSessionsSharedWith(ctx, "bob")
	if err != nil {
		t.Fatalf("list shared: %v", err)
	}
	if len(shared) != 2 {
		t.Fatalf("expected 2 shared sessions for bob, got %d", len(shared))
	}
	seen := map[string]SharedSession{}
	for _, entry := range shared {
		seen[entry.Session.ID] = entry
		if entry.SharedBy != "alice" {
			t.Fatalf("expected shared_by alice, got %q", entry.SharedBy)
		}
		// The joined session row must be fully hydrated, not a stub.
		if entry.Session.UserID != "alice" || entry.Session.Status != "created" || entry.Session.Mode != "agent" {
			t.Fatalf("session row not hydrated: %+v", entry.Session)
		}
		if entry.Session.CreatedAt.IsZero() {
			t.Fatal("expected created_at to be hydrated from the joined session row")
		}
	}
	if seen["sess_2"].Visibility != ShareVisibilitySummary {
		t.Fatalf("expected sess_2 visibility summary, got %q", seen["sess_2"].Visibility)
	}
	if len(st.mustListSharedIDs(t, "dave")) != 0 {
		t.Fatal("expected no shared sessions for an unrelated user")
	}
}

func (s *Store) mustListSharedIDs(t *testing.T, viewer string) []string {
	t.Helper()
	shared, err := s.ListSessionsSharedWith(context.Background(), viewer)
	if err != nil {
		t.Fatalf("list shared for %s: %v", viewer, err)
	}
	ids := make([]string, 0, len(shared))
	for _, entry := range shared {
		ids = append(ids, entry.Session.ID)
	}
	return ids
}

// Username reuse is the hole this closes: every row referencing the name must
// stop being live, whether the user was the grantee, the grantor, or the creator
// of a public link.
func TestRevokeSharesForUserCoversGranteeGrantorAndLinks(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	seedShareSession(t, st, "sess_2", "bob")
	seedShareSession(t, st, "sess_3", "carol")

	// bob as grantee, bob as grantor, and an unrelated grant that must survive.
	if err := st.CreateSessionShare(ctx, newTestShare("share_1", "sess_1", "bob", "alice", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share 1: %v", err)
	}
	if err := st.CreateSessionShare(ctx, newTestShare("share_2", "sess_2", "carol", "bob", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share 2: %v", err)
	}
	if err := st.CreateSessionShare(ctx, newTestShare("share_3", "sess_3", "alice", "carol", ShareVisibilityFull)); err != nil {
		t.Fatalf("create share 3: %v", err)
	}
	now := time.Now().UTC()
	if err := st.CreateShareLink(ctx, ShareLink{
		ID: "link_bob", TokenHash: "hash_bob", SessionID: "sess_2", Visibility: ShareVisibilitySummary,
		CreatedBy: "bob", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, nil); err != nil {
		t.Fatalf("create bob link: %v", err)
	}
	if err := st.CreateShareLink(ctx, ShareLink{
		ID: "link_carol", TokenHash: "hash_carol", SessionID: "sess_3", Visibility: ShareVisibilitySummary,
		CreatedBy: "carol", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, nil); err != nil {
		t.Fatalf("create carol link: %v", err)
	}

	revoked, err := st.RevokeSharesForUser(ctx, "bob", now)
	if err != nil {
		t.Fatalf("revoke shares for bob: %v", err)
	}
	if revoked != 3 {
		t.Fatalf("expected 3 rows revoked (grantee, grantor, link), got %d", revoked)
	}

	if _, ok, err := st.ResolveSessionShare(ctx, "sess_1", "bob"); err != nil || ok {
		t.Fatalf("expected bob's inbound grant revoked: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.ResolveSessionShare(ctx, "sess_2", "carol"); err != nil || ok {
		t.Fatalf("expected bob-created grant revoked: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.ResolveSessionShare(ctx, "sess_3", "alice"); err != nil || !ok {
		t.Fatalf("expected unrelated grant to survive: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.LookupShareLink(ctx, "hash_bob", now); err != nil || ok {
		t.Fatalf("expected bob's link revoked: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.LookupShareLink(ctx, "hash_carol", now); err != nil || !ok {
		t.Fatalf("expected carol's link to survive: ok=%v err=%v", ok, err)
	}
}
