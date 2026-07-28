package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShareLinkLookupRejectsExpiredAndRevoked(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	now := time.Now().UTC()

	links := []ShareLink{
		{ID: "link_live", TokenHash: "hash_live", SessionID: "sess_1", Visibility: ShareVisibilitySummary,
			CreatedBy: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "link_expired", TokenHash: "hash_expired", SessionID: "sess_1", Visibility: ShareVisibilitySummary,
			CreatedBy: "alice", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
		{ID: "link_revoked", TokenHash: "hash_revoked", SessionID: "sess_1", Visibility: ShareVisibilitySummary,
			CreatedBy: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	for _, link := range links {
		if err := st.CreateShareLink(ctx, link, nil); err != nil {
			t.Fatalf("create %s: %v", link.ID, err)
		}
	}
	if err := st.RevokeShareLink(ctx, RevokeShareParams{SessionID: "sess_1", ID: "link_revoked", Now: now}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, ok, err := st.LookupShareLink(ctx, "hash_live", now); err != nil || !ok {
		t.Fatalf("expected live link to resolve: ok=%v err=%v", ok, err)
	}
	for _, tokenHash := range []string{"hash_expired", "hash_revoked", "hash_unknown"} {
		if _, ok, err := st.LookupShareLink(ctx, tokenHash, now); err != nil || ok {
			t.Fatalf("expected %s not to resolve: ok=%v err=%v", tokenHash, ok, err)
		}
	}
}

func TestShareLinkRotateInvalidatesOldToken(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	seedShareSession(t, st, "sess_2", "bob")
	now := time.Now().UTC()

	if err := st.CreateShareLink(ctx, ShareLink{
		ID: "link_1", TokenHash: "hash_old", SessionID: "sess_1", Visibility: ShareVisibilityFull,
		CreatedBy: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, []ShareLinkFile{{Path: "report.html", BlobSHA256: "sha_a", Size: 12}}); err != nil {
		t.Fatalf("create link: %v", err)
	}

	// Cross-session rotate must be refused by the store, not just the route.
	err := st.RotateShareLinkToken(ctx, RevokeShareParams{SessionID: "sess_2", ID: "link_1"}, "hash_attacker")
	if !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("expected ErrShareNotFound for cross-session rotate, got %v", err)
	}

	if err := st.RotateShareLinkToken(ctx, RevokeShareParams{SessionID: "sess_1", ID: "link_1"}, "hash_new"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, ok, err := st.LookupShareLink(ctx, "hash_old", now); err != nil || ok {
		t.Fatalf("expected old token invalidated: ok=%v err=%v", ok, err)
	}
	link, ok, err := st.LookupShareLink(ctx, "hash_new", now)
	if err != nil || !ok {
		t.Fatalf("expected new token to resolve: ok=%v err=%v", ok, err)
	}
	// Rotation must not disturb the whitelist or expiry.
	files, err := st.ListShareLinkFiles(ctx, link.ID)
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "report.html" || files[0].BlobSHA256 != "sha_a" {
		t.Fatalf("whitelist changed by rotate: %+v", files)
	}
}

func TestTouchShareLinkAccessAccumulates(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	now := time.Now().UTC()
	if err := st.CreateShareLink(ctx, ShareLink{
		ID: "link_1", TokenHash: "hash_1", SessionID: "sess_1", Visibility: ShareVisibilitySummary,
		CreatedBy: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, nil); err != nil {
		t.Fatalf("create link: %v", err)
	}

	if err := st.TouchShareLinkAccess(ctx, []ShareLinkAccess{{LinkID: "link_1", Count: 3, LastSeen: now}}); err != nil {
		t.Fatalf("first flush: %v", err)
	}
	later := now.Add(time.Minute)
	if err := st.TouchShareLinkAccess(ctx, []ShareLinkAccess{{LinkID: "link_1", Count: 2, LastSeen: later}}); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if err := st.TouchShareLinkAccess(ctx, nil); err != nil {
		t.Fatalf("empty flush should be a no-op: %v", err)
	}

	link, ok, err := st.LookupShareLink(ctx, "hash_1", now)
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if link.AccessCount != 5 {
		t.Fatalf("expected access_count 5, got %d", link.AccessCount)
	}
	if link.LastAccessedAt == nil || !link.LastAccessedAt.Equal(later) {
		t.Fatalf("expected last_accessed_at %v, got %v", later, link.LastAccessedAt)
	}
}

// The sweep must report only digests that lost their LAST reference: a blob
// still held by a surviving link must not be handed to the unlinker.
func TestSweepExpiredShareLinksReturnsOnlyOrphanedBlobs(t *testing.T) {
	ctx := context.Background()
	st := newShareTestStore(t)
	seedShareSession(t, st, "sess_1", "alice")
	now := time.Now().UTC()

	// shared_blob is referenced by both an expired and a live link.
	// lonely_blob is referenced only by expired links.
	if err := st.CreateShareLink(ctx, ShareLink{
		ID: "link_expired", TokenHash: "hash_expired", SessionID: "sess_1", Visibility: ShareVisibilitySummary,
		CreatedBy: "alice", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}, []ShareLinkFile{
		{Path: "a.html", BlobSHA256: "shared_blob", Size: 1},
		{Path: "b.html", BlobSHA256: "lonely_blob", Size: 2},
	}); err != nil {
		t.Fatalf("create expired link: %v", err)
	}
	if err := st.CreateShareLink(ctx, ShareLink{
		ID: "link_live", TokenHash: "hash_live", SessionID: "sess_1", Visibility: ShareVisibilitySummary,
		CreatedBy: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, []ShareLinkFile{{Path: "c.html", BlobSHA256: "shared_blob", Size: 1}}); err != nil {
		t.Fatalf("create live link: %v", err)
	}

	orphaned, err := st.SweepExpiredShareLinks(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != "lonely_blob" {
		t.Fatalf("expected only lonely_blob orphaned, got %v", orphaned)
	}
	if _, ok, err := st.LookupShareLink(ctx, "hash_expired", now); err != nil || ok {
		t.Fatalf("expected expired link deleted: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.LookupShareLink(ctx, "hash_live", now); err != nil || !ok {
		t.Fatalf("expected live link retained: ok=%v err=%v", ok, err)
	}
	files, err := st.ListShareLinkFiles(ctx, "link_expired")
	if err != nil {
		t.Fatalf("list swept files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected swept link's file rows removed, got %d", len(files))
	}
	// Idempotent: a second sweep finds nothing.
	again, err := st.SweepExpiredShareLinks(ctx, now)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected second sweep to be empty, got %v", again)
	}
}
