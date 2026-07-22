package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

// newSearchTestStore opens a fresh store and seeds a session owned by userID.
func newSearchTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, ctx
}

func seedSession(t *testing.T, st *Store, ctx context.Context, id, userID, title string) {
	t.Helper()
	now := time.Now().UTC()
	s := Session{
		ID:        id,
		UserID:    userID,
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Mode:      "agent",
		Workspace: t.TempDir(),
		RestoreID: "r-" + id,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, s); err != nil {
		t.Fatalf("create session %q: %v", id, err)
	}
	if title != "" {
		if err := st.UpdateSessionTitle(ctx, id, title, true); err != nil {
			t.Fatalf("title %q: %v", id, err)
		}
	}
}

func TestSearchMessagesUserScoping(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "")
	seedSession(t, st, ctx, "sess_b", "bob", "")

	if _, err := st.AddMessage(ctx, "sess_a", "user", "alice secret pineapple"); err != nil {
		t.Fatalf("add alice msg: %v", err)
	}
	if _, err := st.AddMessage(ctx, "sess_b", "user", "bob secret pineapple"); err != nil {
		t.Fatalf("add bob msg: %v", err)
	}

	hits, err := st.SearchMessages(ctx, "alice", "pineapple", 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("alice should see only her hit, got %d: %+v", len(hits), hits)
	}
	if hits[0].SessionID != "sess_a" {
		t.Fatalf("alice hit session = %q, want sess_a", hits[0].SessionID)
	}

	// Bob's search must never surface alice's message.
	bobHits, err := st.SearchMessages(ctx, "bob", "pineapple", 50)
	if err != nil {
		t.Fatalf("bob search: %v", err)
	}
	if len(bobHits) != 1 || bobHits[0].SessionID != "sess_b" {
		t.Fatalf("bob should see only his hit, got %+v", bobHits)
	}
}

func TestSearchMessagesTitleAndBody(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "Deployment Notes")

	if _, err := st.AddMessage(ctx, "sess_a", "user", "how do I configure the deployment"); err != nil {
		t.Fatalf("add user msg: %v", err)
	}
	if _, err := st.AddMessage(ctx, "sess_a", "assistant", "you can configure deployment via the manifest"); err != nil {
		t.Fatalf("add assistant msg: %v", err)
	}

	hits, err := st.SearchMessages(ctx, "alice", "deployment", 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var titleHits, userHits, assistantHits int
	for _, h := range hits {
		switch {
		case h.Kind == "title":
			titleHits++
		case h.Kind == "message" && h.Role == "user":
			userHits++
		case h.Kind == "message" && h.Role == "assistant":
			assistantHits++
		}
	}
	if titleHits != 1 {
		t.Fatalf("want 1 title hit, got %d (%+v)", titleHits, hits)
	}
	if userHits != 1 || assistantHits != 1 {
		t.Fatalf("want user+assistant message hits, got user=%d assistant=%d", userHits, assistantHits)
	}

	// Title hits come before message hits.
	if hits[0].Kind != "title" {
		t.Fatalf("first hit should be a title, got %q", hits[0].Kind)
	}
}

func TestSearchMessagesSubstringMidWord(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "")

	if _, err := st.AddMessage(ctx, "sess_a", "assistant", "the refactoring is complete"); err != nil {
		t.Fatalf("add msg: %v", err)
	}

	// "actor" appears mid-word inside "refactoring" (ref-actor-ing). Trigram
	// substring matching should find it even though it is not a token boundary.
	hits, err := st.SearchMessages(ctx, "alice", "actor", 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Kind != "message" {
		t.Fatalf("want one mid-word substring message hit, got %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, SearchSnippetOpen) {
		t.Fatalf("snippet should contain the highlight marker, got %q", hits[0].Snippet)
	}
}

func TestSearchMessagesChineseSubstring(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "")

	if _, err := st.AddMessage(ctx, "sess_a", "user", "请帮我部署这个服务到生产环境"); err != nil {
		t.Fatalf("add msg: %v", err)
	}

	hits, err := st.SearchMessages(ctx, "alice", "部署这个", 50)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want one Chinese substring hit, got %d: %+v", len(hits), hits)
	}
}

func TestSearchMessagesShortQueryTitleOnly(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "ab quickref")

	if _, err := st.AddMessage(ctx, "sess_a", "user", "ab is a two char body"); err != nil {
		t.Fatalf("add msg: %v", err)
	}

	// A 2-rune query is below the trigram floor: it must not error and must
	// match titles only (the message body is skipped).
	hits, err := st.SearchMessages(ctx, "alice", "ab", 50)
	if err != nil {
		t.Fatalf("short query search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 title-only hit for short query, got %d: %+v", len(hits), hits)
	}
	if hits[0].Kind != "title" {
		t.Fatalf("short query hit should be a title, got %q", hits[0].Kind)
	}
}

func TestSearchMessagesEmptyQuery(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "something")

	hits, err := st.SearchMessages(ctx, "alice", "   ", 50)
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty query should return no hits, got %d", len(hits))
	}
}

func TestSearchMessagesBackfillPreExistingRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()

	// First store instance: create the schema (with FTS + triggers) and rows,
	// then simulate a pre-FTS history by clearing the index. Re-opening must
	// rebuild it from the messages table.
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedSession(t, st, ctx, "sess_a", "alice", "")
	if _, err := st.AddMessage(ctx, "sess_a", "user", "historical watermelon message"); err != nil {
		t.Fatalf("add msg: %v", err)
	}
	// Wipe the index to mimic data that predates the FTS vtable.
	if _, err := st.DBForTest().ExecContext(ctx, `INSERT INTO messages_fts(messages_fts) VALUES('delete-all')`); err != nil {
		t.Fatalf("clear index: %v", err)
	}
	// Confirm the index really is empty now (via the docsize shadow table:
	// count() on the vtable itself reads through to the content table).
	var cnt int
	if err := st.DBForTest().QueryRowContext(ctx, `SELECT count(*) FROM messages_fts_docsize`).Scan(&cnt); err != nil {
		t.Fatalf("count fts: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("index should be empty after delete-all, got %d", cnt)
	}
	_ = st.Close()

	// Re-open: migrate() should detect the empty index over non-empty messages
	// and rebuild.
	st2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	hits, err := st2.SearchMessages(ctx, "alice", "watermelon", 50)
	if err != nil {
		t.Fatalf("search after backfill: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("backfill should index pre-existing rows, got %d hits", len(hits))
	}
}

func TestSearchMessagesLimitAndTruncation(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "")

	for i := 0; i < 5; i++ {
		if _, err := st.AddMessage(ctx, "sess_a", "user", "banana message body"); err != nil {
			t.Fatalf("add msg %d: %v", i, err)
		}
	}

	hits, err := st.SearchMessages(ctx, "alice", "banana", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("want limit of 3 hits, got %d", len(hits))
	}
}

func TestSearchMessagesNeutralizesFTSOperators(t *testing.T) {
	st, ctx := newSearchTestStore(t)
	seedSession(t, st, ctx, "sess_a", "alice", "")

	if _, err := st.AddMessage(ctx, "sess_a", "user", "a OR b AND literal query"); err != nil {
		t.Fatalf("add msg: %v", err)
	}

	// Raw FTS operators would either error or match unexpectedly; the quoted
	// phrase must be treated literally.
	hits, err := st.SearchMessages(ctx, "alice", "OR b AND", 50)
	if err != nil {
		t.Fatalf("operator query should not error: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want literal-phrase match, got %d hits", len(hits))
	}
}
