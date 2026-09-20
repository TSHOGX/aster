package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"

	_ "modernc.org/sqlite"
)

func TestPruneEventsAppliesRetentionWindowAndRows(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 5, 25, 10, 0, 0, 123456789, time.UTC)

	t.Run("rows", func(t *testing.T) {
		st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		createStoreSession(t, ctx, st, "sess_a")
		createStoreSession(t, ctx, st, "sess_b")
		firstID := appendStoreTestEvent(t, ctx, st, "sess_a", "first", base)
		secondID := appendStoreTestEvent(t, ctx, st, "sess_b", "second", base.Add(time.Second))
		thirdID := appendStoreTestEvent(t, ctx, st, "sess_a", "third", base.Add(2*time.Second))
		fourthID := appendStoreTestEvent(t, ctx, st, "sess_b", "fourth", base.Add(3*time.Second))

		deleted, err := st.PruneEvents(ctx, PruneEventsParams{
			RetentionRows: 2,
			Now:           base.Add(4 * time.Second),
		})
		if err != nil {
			t.Fatalf("prune by rows: %v", err)
		}
		if deleted != 2 {
			t.Fatalf("deleted=%d want 2", deleted)
		}
		records, err := st.ListEvents(ctx, ListEventsParams{})
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		if got := eventIDs(records); len(got) != 2 || got[0] != thirdID || got[1] != fourthID {
			t.Fatalf("retained ids=%v want [%d %d] after deleting %d/%d", got, thirdID, fourthID, firstID, secondID)
		}
		oldest, ok, err := st.OldestEventID(ctx, "")
		if err != nil || !ok || oldest != thirdID {
			t.Fatalf("oldest global=%d ok=%v err=%v want %d", oldest, ok, err, thirdID)
		}
		oldest, ok, err = st.OldestEventID(ctx, "sess_b")
		if err != nil || !ok || oldest != fourthID {
			t.Fatalf("oldest sess_b=%d ok=%v err=%v want %d", oldest, ok, err, fourthID)
		}
	})

	t.Run("window", func(t *testing.T) {
		st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		createStoreSession(t, ctx, st, "sess_a")
		createStoreSession(t, ctx, st, "sess_b")
		firstID := appendStoreTestEvent(t, ctx, st, "sess_a", "first", base)
		secondID := appendStoreTestEvent(t, ctx, st, "sess_b", "second", base.Add(time.Second))
		thirdID := appendStoreTestEvent(t, ctx, st, "sess_a", "third", base.Add(2*time.Second))
		fourthID := appendStoreTestEvent(t, ctx, st, "sess_b", "fourth", base.Add(3*time.Second))

		deleted, err := st.PruneEvents(ctx, PruneEventsParams{
			RetentionWindow: 2 * time.Second,
			Now:             base.Add(4 * time.Second),
		})
		if err != nil {
			t.Fatalf("prune by window: %v", err)
		}
		if deleted != 2 {
			t.Fatalf("deleted=%d want 2", deleted)
		}
		records, err := st.ListEvents(ctx, ListEventsParams{})
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		if got := eventIDs(records); len(got) != 2 || got[0] != thirdID || got[1] != fourthID {
			t.Fatalf("retained ids=%v want [%d %d] after deleting %d/%d", got, thirdID, fourthID, firstID, secondID)
		}
		oldest, ok, err := st.OldestEventID(ctx, "")
		if err != nil || !ok || oldest != thirdID {
			t.Fatalf("oldest global=%d ok=%v err=%v want %d", oldest, ok, err, thirdID)
		}
	})

	t.Run("invalid params", func(t *testing.T) {
		st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if _, err := st.PruneEvents(ctx, PruneEventsParams{RetentionWindow: -time.Second}); err == nil {
			t.Fatalf("negative retention window should fail")
		}
		if _, err := st.PruneEvents(ctx, PruneEventsParams{RetentionRows: -1}); err == nil {
			t.Fatalf("negative retention rows should fail")
		}
	})
}

func TestOwnerLockContentionAndTamperDetection(t *testing.T) {
	ctx := context.Background()
	runDir := t.TempDir()
	owner, err := AcquireOwnerLock(runDir)
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if second, err := AcquireOwnerLock(runDir); err == nil {
		_ = second.Close()
		t.Fatalf("second owner lock should fail")
	}

	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.WriteOwner(ctx, owner); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	if err := st.AssertOwner(ctx, owner.UUID); err != nil {
		t.Fatalf("assert owner: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE orchestrator_owner SET uuid = 'tampered' WHERE singleton = 1`); err != nil {
		t.Fatalf("tamper owner: %v", err)
	}
	if err := st.AssertOwner(ctx, owner.UUID); err == nil {
		t.Fatalf("tampered owner should fail assertion")
	}
}

func TestSessionActiveGenerationCAS(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	createStoreSession(t, ctx, st, "sess_cas")
	now := time.Now().UTC()
	if _, err := st.db.ExecContext(ctx, `
INSERT INTO runtime_generations (generation_id, session_id, status, lease_owner, lease_expires_at)
VALUES ('gen_a', 'sess_cas', 'idle', 'owner', ?)`, formatTime(now.Add(time.Minute))); err != nil {
		t.Fatalf("insert gen_a: %v", err)
	}
	if err := st.UpdateSessionActiveGeneration(ctx, SessionActiveGenerationCASParams{
		SessionID:        "sess_cas",
		NextGenerationID: "gen_a",
	}); err != nil {
		t.Fatalf("initial update active generation: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
UPDATE runtime_generations SET status = 'failed' WHERE generation_id = 'gen_a'`); err != nil {
		t.Fatalf("mark gen_a terminal: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `
INSERT INTO runtime_generations (generation_id, session_id, status, lease_owner, lease_expires_at)
VALUES ('gen_b', 'sess_cas', 'idle', 'owner', ?)`, formatTime(now.Add(time.Minute))); err != nil {
		t.Fatalf("insert gen_b: %v", err)
	}
	if err := st.UpdateSessionActiveGeneration(ctx, SessionActiveGenerationCASParams{
		SessionID:            "sess_cas",
		ExpectedGenerationID: sql.NullString{String: "gen_a", Valid: true},
		NextGenerationID:     "gen_b",
	}); err != nil {
		t.Fatalf("update active generation with expected value: %v", err)
	}
	if err := st.UpdateSessionActiveGeneration(ctx, SessionActiveGenerationCASParams{
		SessionID:            "sess_cas",
		ExpectedGenerationID: sql.NullString{String: "gen_a", Valid: true},
		NextGenerationID:     "gen_c",
	}); err == nil {
		t.Fatalf("stale expected generation should fail")
	}
	var activeGeneration string
	if err := st.db.QueryRowContext(ctx, `SELECT active_generation_id FROM sessions WHERE id = 'sess_cas'`).Scan(&activeGeneration); err != nil {
		t.Fatalf("query active generation: %v", err)
	}
	if activeGeneration != "gen_b" {
		t.Fatalf("unexpected active generation: %s", activeGeneration)
	}
}

func createStoreSession(t *testing.T, ctx context.Context, st *Store, id string) {
	t.Helper()
	createStoreSessionWithAgent(t, ctx, st, id, "pi")
}

func createStoreSessionWithAgent(t *testing.T, ctx context.Context, st *Store, id, agent string) {
	createStoreSessionBackend(t, ctx, st, id, agent, "legacy")
}

func createStoreSessionBackend(t *testing.T, ctx context.Context, st *Store, id, agent, backend string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, Session{

		ID:        id,
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     agent,
		Workspace: filepath.Join(t.TempDir(), id),

		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
}

func appendStoreTestEvent(t *testing.T, ctx context.Context, st *Store, sessionID, name string, now time.Time) int64 {
	t.Helper()
	eventID, err := st.AppendEvent(ctx, AppendEventParams{
		SessionID: sessionID,
		Type:      "test.event",
		Payload:   map[string]string{"name": name},
		Now:       now,
	})
	if err != nil {
		t.Fatalf("append event %s: %v", name, err)
	}
	return eventID
}

func eventIDs(records []EventRecord) []int64 {
	ids := make([]int64, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.EventID)
	}
	return ids
}
