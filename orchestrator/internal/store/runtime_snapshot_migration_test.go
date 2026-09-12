package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

// A pre-existing control database must upgrade cleanly: the phase columns and the
// revision triggers are added by ensureTurnColumns, and an ALTER-time mistake here
// has previously produced a crash loop that only affected already-deployed
// databases. Simulate that by dropping the new columns/triggers and reopening.
func TestUpgradeFromPreRuntimeProjectionSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	session := Session{ID: "sess_old", UserID: "lab", Status: string(sessionstate.RunningIdle),
		Agent: "pi", Workspace: t.TempDir(), RestoreID: "r_old", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	enq, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: session.ID, Content: "legacy turn", Now: now})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Regress turns to what shipped before this change. sessions.turn_revision is
	// left in place: SQLite refuses to drop a column while other schema objects
	// are being revalidated, and that column arrives through ensureSessionColumns,
	// the same ALTER-with-constant-default path title/title_custom already use.
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS turns_revision_insert`,
		`DROP TRIGGER IF EXISTS turns_revision_update`,
		`DROP TRIGGER IF EXISTS turns_revision_delete`,
		`ALTER TABLE turns DROP COLUMN phase`,
		`ALTER TABLE turns DROP COLUMN phase_at`,
		`ALTER TABLE turns DROP COLUMN phase_tool`,
		`ALTER TABLE turns DROP COLUMN phase_detail`,
	} {
		if _, err := st.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("regress schema (%s): %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening runs the migration. It must succeed and leave old rows readable.
	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen (migration failed): %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	snapshot, err := st2.GetSessionRuntimeSnapshot(ctx, session.ID)
	if err != nil {
		t.Fatalf("snapshot after migration: %v", err)
	}
	if len(snapshot.QueuedTurns) != 1 || snapshot.QueuedTurns[0].ID != enq.TurnID {
		t.Fatalf("legacy turn lost: %+v", snapshot)
	}
	// The triggers must be live again, or revisions would silently stop advancing.
	before := snapshot.Revision
	second, err := st2.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{SessionID: session.ID, Content: "post-migration", Now: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("enqueue after migration: %v", err)
	}
	if second.Runtime.Revision <= before {
		t.Fatalf("revision trigger not restored: %d -> %d", before, second.Runtime.Revision)
	}
	// Migration is idempotent: a redeploy or rollback-and-forward must be safe.
	if err := st2.migrate(ctx); err != nil {
		t.Fatalf("re-running migration: %v", err)
	}
}
