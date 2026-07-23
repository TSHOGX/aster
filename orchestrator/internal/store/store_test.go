package store

import (
	"context"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
)

func TestModeForDriverUsesDriverRegistryKind(t *testing.T) {
	tests := []struct {
		driverID string
		want     string
	}{
		{driverID: "pi", want: "agent"},
		{driverID: "sh", want: "shell"},
		{driverID: "unknown", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.driverID, func(t *testing.T) {
			if got := ModeForDriver(tt.driverID); got != tt.want {
				t.Fatalf("ModeForDriver(%q)=%q want %q", tt.driverID, got, tt.want)
			}
		})
	}
}

func TestListMessagesAndUUID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	want := Session{
		ID:        "sess_1",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, want); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := st.GetSession(ctx, want.ID); err != nil {
		t.Fatalf("get session: %v", err)
	}

	if _, err := st.AddMessage(ctx, want.ID, "user", "hello"); err != nil {
		t.Fatalf("add user: %v", err)
	}
	if _, err := st.AddMessage(ctx, want.ID, "assistant", "world"); err != nil {
		t.Fatalf("add assistant: %v", err)
	}

	msgs, err := st.ListMessages(ctx, want.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("msg[0] mismatch: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "world" {
		t.Fatalf("msg[1] mismatch: %+v", msgs[1])
	}
	if msgs[0].ID >= msgs[1].ID {
		t.Fatalf("messages should be ordered by id ascending")
	}
}

func TestMessageTraceAndFeedback(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	sess := Session{
		ID: "sess_t", UserID: "lab", Status: string(sessionstate.Created),
		Agent: "pi", Workspace: dir, RestoreID: "r", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	msg, err := st.AddAssistantMessage(ctx, sess.ID, "assistant", "done", "stop")
	if err != nil {
		t.Fatalf("add message: %v", err)
	}
	if msg.StopReason != "stop" {
		t.Fatalf("stop_reason want stop, got %q", msg.StopReason)
	}

	// Trace: empty/[] is a no-op; a real array round-trips verbatim.
	if err := st.SaveMessageTrace(ctx, msg.ID, sess.ID, nil); err != nil {
		t.Fatalf("save nil trace: %v", err)
	}
	if err := st.SaveMessageTrace(ctx, msg.ID, sess.ID, []byte("[]")); err != nil {
		t.Fatalf("save empty trace: %v", err)
	}
	nodes := []byte(`[{"kind":"tool","toolName":"read","label":"a.txt"}]`)
	if err := st.SaveMessageTrace(ctx, msg.ID, sess.ID, nodes); err != nil {
		t.Fatalf("save trace: %v", err)
	}

	// Feedback: set, toggle value, then clear.
	if got, err := st.SetMessageFeedback(ctx, msg.ID, "lab", "up"); err != nil || got != "up" {
		t.Fatalf("set up: got %q err %v", got, err)
	}
	if got, err := st.SetMessageFeedback(ctx, msg.ID, "lab", "down"); err != nil || got != "down" {
		t.Fatalf("set down: got %q err %v", got, err)
	}
	if _, err := st.SetMessageFeedback(ctx, msg.ID, "lab", "sideways"); err == nil {
		t.Fatalf("expected error for invalid rating")
	}

	msgs, err := st.ListMessagesForUser(ctx, sess.ID, "lab")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("list for user: len %d err %v", len(msgs), err)
	}
	if string(msgs[0].Trace) != string(nodes) {
		t.Fatalf("trace mismatch: %s", msgs[0].Trace)
	}
	if msgs[0].StopReason != "stop" {
		t.Fatalf("stop_reason round-trip want stop, got %q", msgs[0].StopReason)
	}
	if msgs[0].Feedback != "down" {
		t.Fatalf("feedback want down, got %q", msgs[0].Feedback)
	}

	// A different user sees the trace but not lab's feedback.
	other, err := st.ListMessagesForUser(ctx, sess.ID, "someone-else")
	if err != nil || len(other) != 1 {
		t.Fatalf("list other: %v", err)
	}
	if other[0].Feedback != "" {
		t.Fatalf("other user feedback should be empty, got %q", other[0].Feedback)
	}
	if len(other[0].Trace) == 0 {
		t.Fatalf("other user should still see trace")
	}

	// Clear feedback removes it.
	if got, err := st.SetMessageFeedback(ctx, msg.ID, "lab", ""); err != nil || got != "" {
		t.Fatalf("clear: got %q err %v", got, err)
	}
	msgs, _ = st.ListMessagesForUser(ctx, sess.ID, "lab")
	if msgs[0].Feedback != "" {
		t.Fatalf("feedback should be cleared, got %q", msgs[0].Feedback)
	}

	// MessageSession resolves ownership; unknown id => not ok.
	if sid, ok, err := st.MessageSession(ctx, msg.ID); err != nil || !ok || sid != sess.ID {
		t.Fatalf("message session: sid %q ok %v err %v", sid, ok, err)
	}
	if _, ok, err := st.MessageSession(ctx, 999999); err != nil || ok {
		t.Fatalf("unknown message should be not-ok: ok %v err %v", ok, err)
	}
}

func TestUpdateSessionTitle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_title",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_title",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A fresh session has an empty, non-custom title.
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "" || got.TitleCustom {
		t.Fatalf("fresh session title = %q custom = %v, want empty/false", got.Title, got.TitleCustom)
	}

	// Automatic naming (custom=false) sets the title and leaves it non-custom.
	if err := st.UpdateSessionTitle(ctx, session.ID, "auto placeholder", false); err != nil {
		t.Fatalf("auto title: %v", err)
	}
	got, _ = st.GetSession(ctx, session.ID)
	if got.Title != "auto placeholder" || got.TitleCustom {
		t.Fatalf("after auto title = %q custom = %v", got.Title, got.TitleCustom)
	}

	// A manual rename (custom=true) marks the title user-authored.
	if err := st.UpdateSessionTitle(ctx, session.ID, "my name", true); err != nil {
		t.Fatalf("manual title: %v", err)
	}
	got, _ = st.GetSession(ctx, session.ID)
	if got.Title != "my name" || !got.TitleCustom {
		t.Fatalf("after manual title = %q custom = %v", got.Title, got.TitleCustom)
	}

	// A subsequent automatic naming must NOT clobber the user's title.
	if err := st.UpdateSessionTitle(ctx, session.ID, "auto again", false); err != nil {
		t.Fatalf("auto title over custom: %v", err)
	}
	got, _ = st.GetSession(ctx, session.ID)
	if got.Title != "my name" || !got.TitleCustom {
		t.Fatalf("auto naming clobbered custom title: %q custom = %v", got.Title, got.TitleCustom)
	}
}

// TestEnsureSessionColumnsIdempotent simulates an older database whose sessions
// table predates the title columns, and verifies ensureSchema adds them without
// error and that a second run is a no-op.
func TestEnsureSessionColumnsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Drop the columns to emulate a legacy schema, then re-run the migration.
	for _, col := range []string{"title", "title_custom"} {
		if _, err := st.db.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN `+col); err != nil {
			t.Fatalf("drop column %s: %v", col, err)
		}
	}
	if exists, err := st.columnExists(ctx, "sessions", "title"); err != nil || exists {
		t.Fatalf("expected title column absent after drop (err=%v exists=%v)", err, exists)
	}

	// First migration adds the columns.
	if err := st.ensureSessionColumns(ctx); err != nil {
		t.Fatalf("ensureSessionColumns (add): %v", err)
	}
	if exists, err := st.columnExists(ctx, "sessions", "title"); err != nil || !exists {
		t.Fatalf("expected title column present after migration (err=%v exists=%v)", err, exists)
	}
	// Second run is idempotent.
	if err := st.ensureSessionColumns(ctx); err != nil {
		t.Fatalf("ensureSessionColumns (idempotent): %v", err)
	}
}

func TestUpdateSessionStatusAndActivity(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_test",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_test",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Update status and activity
	activityTime := now.Add(5 * time.Minute)
	restoreMS := int64(250)
	if err := st.UpdateSessionStatusAndActivity(ctx, session.ID, string(sessionstate.RunningIdle), &restoreMS, activityTime); err != nil {
		t.Fatalf("update status and activity: %v", err)
	}

	// Verify update
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != string(sessionstate.RunningIdle) {
		t.Errorf("status: want %s, got %s", sessionstate.RunningIdle, got.Status)
	}
	if got.RestoreMS == nil || *got.RestoreMS != 250 {
		t.Errorf("restore_ms: want 250, got %v", got.RestoreMS)
	}
	if got.LastActivityAt == nil {
		t.Fatalf("last_activity_at should not be nil")
	}
	if got.LastActivityAt.Sub(activityTime).Abs() > time.Second {
		t.Errorf("last_activity_at: want %v, got %v", activityTime, *got.LastActivityAt)
	}
}

func TestFailSessionStoresTypedFailure(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_fail",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_fail",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	failedAt := now.Add(10 * time.Second)
	if err := st.FailSession(ctx, FailSessionParams{
		SessionID:    session.ID,
		ErrorClass:   "probe_failed_pre_start",
		Reason:       "pre-start sandbox network probe failed",
		LastActivity: failedAt,
		Now:          failedAt,
	}); err != nil {
		t.Fatalf("fail session: %v", err)
	}

	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != string(sessionstate.Failed) ||
		got.ErrorClass != "probe_failed_pre_start" ||
		got.FailureReason != "pre-start sandbox network probe failed" {
		t.Fatalf("unexpected failed session: %+v", got)
	}
	if got.EndedAt == nil {
		t.Fatalf("ended_at should be set")
	}
}

func TestEnqueueTurnMessageCreatesQueuedTurnMessageAndActivatesSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_enqueue",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_enqueue",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	result, err := st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: session.ID,
		Content:   "hello",
		Now:       now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("enqueue turn message: %v", err)
	}
	if result.TurnID == 0 || result.Message.ID == 0 {
		t.Fatalf("expected ids to be assigned: %+v", result)
	}
	if result.Message.Role != "user" || result.Message.Content != "hello" {
		t.Fatalf("unexpected message: %+v", result.Message)
	}

	var turnStatus, turnContent string
	var turnSequence int64
	if err := st.db.QueryRowContext(ctx, `
SELECT sequence, content, status
FROM turns
WHERE id = ?`, result.TurnID).Scan(&turnSequence, &turnContent, &turnStatus); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if turnSequence != 1 || turnContent != "hello" || turnStatus != "queued" {
		t.Fatalf("unexpected queued turn: sequence=%d content=%q status=%q", turnSequence, turnContent, turnStatus)
	}
	got, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != string(sessionstate.RunningActive) || got.LastActivityAt == nil {
		t.Fatalf("session not marked active: %+v", got)
	}
}

func TestEnqueueTurnMessageRejectsBusySessionWithoutWrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_busy_enqueue",
		UserID:    "lab",
		Status:    string(sessionstate.RunningActive),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_busy_enqueue",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = st.EnqueueTurnMessage(ctx, EnqueueTurnMessageParams{
		SessionID: session.ID,
		Content:   "hello",
		Now:       now.Add(time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), "session cannot accept input") {
		t.Fatalf("expected session cannot accept input, got %v", err)
	}

	var turns, messages int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE session_id = ?`, session.ID).Scan(&turns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id = ?`, session.ID).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if turns != 0 || messages != 0 {
		t.Fatalf("busy enqueue should roll back writes: turns=%d messages=%d", turns, messages)
	}
}

func TestListSessionsByStatus(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()

	// Create sessions with different statuses
	sessions := []Session{
		{
			ID:        "sess_1",
			UserID:    "lab",
			Status:    string(sessionstate.RunningIdle),
			Agent:     "pi",
			Workspace: dir,
			RestoreID: "phase3-sess_1",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "sess_2",
			UserID:    "lab",
			Status:    string(sessionstate.RunningActive),
			Agent:     "pi",
			Workspace: dir,
			RestoreID: "phase3-sess_2",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "sess_3",
			UserID:    "lab",
			Status:    string(sessionstate.RunningIdle),
			Agent:     "pi",
			Workspace: dir,
			RestoreID: "phase3-sess_3",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	for _, s := range sessions {
		if err := st.CreateSession(ctx, s); err != nil {
			t.Fatalf("create session %s: %v", s.ID, err)
		}
	}

	// Update activity times
	activity1 := now.Add(-10 * time.Minute)
	activity3 := now.Add(-35 * time.Minute)
	if err := st.UpdateSessionStatusAndActivity(ctx, "sess_1", string(sessionstate.RunningIdle), nil, activity1); err != nil {
		t.Fatalf("update sess_1: %v", err)
	}
	if err := st.UpdateSessionStatusAndActivity(ctx, "sess_3", string(sessionstate.RunningIdle), nil, activity3); err != nil {
		t.Fatalf("update sess_3: %v", err)
	}

	// List idle sessions
	idleSessions, err := st.ListSessionsByStatus(ctx, string(sessionstate.RunningIdle))
	if err != nil {
		t.Fatalf("list sessions by status: %v", err)
	}

	if len(idleSessions) != 2 {
		t.Fatalf("want 2 idle sessions, got %d", len(idleSessions))
	}

	// Should be ordered by last_activity_at ASC (oldest first)
	if idleSessions[0].ID != "sess_3" {
		t.Errorf("first idle session should be sess_3, got %s", idleSessions[0].ID)
	}
	if idleSessions[1].ID != "sess_1" {
		t.Errorf("second idle session should be sess_1, got %s", idleSessions[1].ID)
	}
}

func TestRejectsLegacyStatuses(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_legacy",
		UserID:    "lab",
		Status:    "idle",
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_legacy",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err == nil {
		t.Fatalf("create session with legacy status should fail")
	}
	if err := st.UpdateSessionStatus(ctx, "sess_legacy", "completed", nil); err == nil {
		t.Fatalf("update to legacy status should fail")
	}
	if _, err := st.ListSessionsByStatus(ctx, "running"); err == nil {
		t.Fatalf("listing legacy status should fail")
	}
}

func TestCountActiveSessionsUsesCanonicalStatuses(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	statuses := []sessionstate.Status{
		sessionstate.Created,
		sessionstate.RunningActive,
		sessionstate.RunningIdle,
		sessionstate.Checkpointing,
		sessionstate.Checkpointed,
		sessionstate.Failed,
		sessionstate.Destroyed,
	}
	for i, status := range statuses {
		id := "sess_count_" + string(rune('a'+i))
		session := Session{
			ID:        id,
			UserID:    "lab",
			Status:    string(status),
			Agent:     "pi",
			Workspace: dir,
			RestoreID: "phase3-" + id,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := st.CreateSession(ctx, session); err != nil {
			t.Fatalf("create session %s: %v", status, err)
		}
	}

	count, err := st.CountActiveSessions(ctx)
	if err != nil {
		t.Fatalf("count active sessions: %v", err)
	}
	if count != 5 {
		t.Fatalf("want 5 active sessions, got %d", count)
	}
}

func TestCountSandboxOccupyingSessionsExcludesReleasedSandboxes(t *testing.T) {
	ctx := context.Background()
	st, owner := openOwnedStore(t, ctx)
	cfg := testAllocatorConfig(t)
	// Widen the pool: several generations are allocated concurrently below and
	// checkpointed/reclaimable ones keep holding their network slot.
	cfg.CIDRPool = netip.MustParsePrefix("10.240.0.0/24")

	// Each resource_state maps to whether the session still occupies a sandbox.
	// allocating/ready/live/recreating hold compute; reserved_checkpointed and
	// reclaimable have released it; a session with no generation holds nothing.
	cases := []struct {
		id            string
		resourceState string // "" means no generation allocated
		occupies      bool
	}{
		{"sess_allocating", "allocating", true},
		{"sess_ready", "ready", true},
		{"sess_live", "live", true},
		{"sess_recreating", "recreating", true},
		{"sess_reserved", "reserved_checkpointed", false},
		{"sess_reclaimable", "reclaimable", false},
		{"sess_no_gen", "", false},
	}

	wantCount := 0
	for _, tc := range cases {
		createStoreSession(t, ctx, st, tc.id)
		if tc.occupies {
			wantCount++
		}
		if tc.resourceState == "" {
			continue
		}
		alloc, err := st.AllocateGeneration(ctx, AllocateGenerationParams{
			SessionID: tc.id,
			Owner:     GenerationLeaseOwner(owner.UUID),
			LeaseTTL:  time.Minute,
			Now:       time.Now().UTC(),
			Config:    cfg,
		})
		if err != nil {
			t.Fatalf("allocate %s: %v", tc.id, err)
		}
		// AllocateGeneration links active_generation_id itself; just move the
		// resource into the state under test.
		if _, err := st.db.ExecContext(ctx,
			`UPDATE runtime_generation_resources SET resource_state = ? WHERE generation_id = ?`,
			tc.resourceState, alloc.GenerationID); err != nil {
			t.Fatalf("set resource state %s: %v", tc.id, err)
		}
	}

	count, err := st.CountSandboxOccupyingSessions(ctx)
	if err != nil {
		t.Fatalf("count sandbox occupying sessions: %v", err)
	}
	if count != wantCount {
		t.Fatalf("want %d sandbox-occupying sessions, got %d", wantCount, count)
	}
}

func TestDeleteArtifactPathDeletesFileAndDescendants(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := Session{
		ID:        "sess_artifacts",
		UserID:    "lab",
		Status:    string(sessionstate.Created),
		Agent:     "pi",
		Workspace: dir,
		RestoreID: "phase3-sess_artifacts",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	for _, path := range []string{"keep.txt", "dir/a.txt", "dir/nested/b.txt", "dir2/a.txt"} {
		if err := st.UpsertArtifact(ctx, Artifact{
			SessionID: session.ID,
			Path:      path,
			Size:      int64(len(path)),
			ModTime:   now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", path, err)
		}
	}

	if err := st.DeleteArtifactPath(ctx, session.ID, "dir"); err != nil {
		t.Fatalf("delete dir: %v", err)
	}

	got, err := st.ListArtifacts(ctx, session.ID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 artifacts after prefix delete, got %+v", got)
	}
	if got[0].Path != "dir2/a.txt" || got[1].Path != "keep.txt" {
		t.Fatalf("unexpected artifacts after prefix delete: %+v", got)
	}

	if err := st.DeleteArtifactPath(ctx, session.ID, "keep.txt"); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	got, err = st.ListArtifacts(ctx, session.ID)
	if err != nil {
		t.Fatalf("list artifacts after file delete: %v", err)
	}
	if len(got) != 1 || got[0].Path != "dir2/a.txt" {
		t.Fatalf("unexpected artifacts after file delete: %+v", got)
	}
}

func TestAlignAutoCheckpointEnabled(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	// idle + created + checkpointed are eligible; running_active (busy) and
	// destroyed (terminal) must be left untouched. All start with the flag off.
	sessions := []Session{
		{ID: "sess_idle", Status: string(sessionstate.RunningIdle)},
		{ID: "sess_created", Status: string(sessionstate.Created)},
		{ID: "sess_ckpt", Status: string(sessionstate.Checkpointed)},
		{ID: "sess_active", Status: string(sessionstate.RunningActive)},
		{ID: "sess_destroyed", Status: string(sessionstate.Destroyed)},
	}
	for _, s := range sessions {
		s.UserID = "lab"
		s.Agent = "pi"
		s.Workspace = dir
		s.RestoreID = "restore-" + s.ID
		s.CreatedAt = now
		s.UpdatedAt = now
		s.AutoCheckpointEnabled = false
		if err := st.CreateSession(ctx, s); err != nil {
			t.Fatalf("create %s: %v", s.ID, err)
		}
	}

	aligned, err := st.AlignAutoCheckpointEnabled(ctx, true)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if aligned != 3 {
		t.Fatalf("aligned: want 3, got %d", aligned)
	}

	want := map[string]bool{
		"sess_idle":      true,
		"sess_created":   true,
		"sess_ckpt":      true,
		"sess_active":    false,
		"sess_destroyed": false,
	}
	for id, expect := range want {
		got, err := st.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.AutoCheckpointEnabled != expect {
			t.Errorf("%s auto_checkpoint_enabled: want %v, got %v", id, expect, got.AutoCheckpointEnabled)
		}
	}

	// Idempotent: a second run with the same value updates nothing.
	aligned, err = st.AlignAutoCheckpointEnabled(ctx, true)
	if err != nil {
		t.Fatalf("align (second): %v", err)
	}
	if aligned != 0 {
		t.Fatalf("second align: want 0, got %d", aligned)
	}
}
