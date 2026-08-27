package server

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// oomRuntime reports the sandbox as OOM-killed, as the memcg counter would.
type oomRuntime struct {
	recordingRuntime
}

func (r *oomRuntime) SandboxOOMKilled(context.Context, string) (bool, bool) {
	return true, true
}

// The regression this guards: in the 2026-08-19 incident the user's last visible
// message was the agent starting a long job, followed by 22 hours of silence. The
// turn carried error_class "unknown_after_ack_started", nothing rendered it, and
// no transcript entry was ever written — so the UI could not explain the
// interruption even though the memcg knew the workload had been OOM-killed.
func TestRecoveryWritesOOMNoticeIntoTranscript(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	session := createServerTestSession(t, ctx, st, dir, "sess_oom_notice", string(sessionstate.RunningIdle), now.Add(-2*time.Minute), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-3 * time.Minute),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    lease_owner = ?,
    lease_expires_at = ?
WHERE generation_id = ?`, store.GenerationLeaseOwner("previous-owner"), now.Add(-time.Minute).Format(time.RFC3339Nano), allocation.GenerationID); err != nil {
		t.Fatalf("expire generation: %v", err)
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: &oomRuntime{},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	if _, err := srv.RecoverExpiredRuntimeResources(ctx, now); err != nil {
		t.Fatalf("recover expired runtime resources: %v", err)
	}

	messages, err := st.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("expected a sandbox-death notice in the transcript, found no messages")
	}
	last := messages[len(messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("notice role = %q, want assistant", last.Role)
	}
	// The dedicated stop_reason is what lets the UI offer the right recovery: the
	// agent's context is gone, unlike after a model-call failure.
	if last.StopReason != sandboxDeathStopReason {
		t.Fatalf("notice stop_reason = %q, want %q", last.StopReason, sandboxDeathStopReason)
	}
	if !strings.Contains(last.Content, "内存") {
		t.Fatalf("notice should name the memory limit as the cause, got %q", last.Content)
	}
	// The user needs to know the agent's context did not survive, or "continue"
	// looks like it resumes in place.
	if !strings.Contains(last.Content, "已丢失") || !strings.Contains(last.Content, "已保留") {
		t.Fatalf("notice must state what survived and what did not, got %q", last.Content)
	}

	// The durable record must carry the diagnosed cause, not the routing class:
	// support reads this long after the transcript scrolls away.
	var generationErrorClass string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COALESCE(error_class, '') FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&generationErrorClass); err != nil {
		t.Fatalf("query generation error class: %v", err)
	}
	if generationErrorClass != sandboxOOMCause {
		t.Fatalf("generation error_class = %q, want %q", generationErrorClass, sandboxOOMCause)
	}
}

// When the cgroup cannot answer, the notice must stay generic rather than assert a
// cause we cannot support.
func TestRecoveryNoticeStaysGenericWhenCauseUnknown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	session := createServerTestSession(t, ctx, st, dir, "sess_generic_notice", string(sessionstate.RunningIdle), now.Add(-2*time.Minute), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-3 * time.Minute),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    lease_owner = ?,
    lease_expires_at = ?
WHERE generation_id = ?`, store.GenerationLeaseOwner("previous-owner"), now.Add(-time.Minute).Format(time.RFC3339Nano), allocation.GenerationID); err != nil {
		t.Fatalf("expire generation: %v", err)
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: &recordingRuntime{}, // reports OOM as unknown
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	if _, err := srv.RecoverExpiredRuntimeResources(ctx, now); err != nil {
		t.Fatalf("recover expired runtime resources: %v", err)
	}

	messages, err := st.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("expected a notice even when the cause is unknown")
	}
	last := messages[len(messages)-1]
	if strings.Contains(last.Content, "内存上限") {
		t.Fatalf("must not claim an OOM without evidence, got %q", last.Content)
	}
	var generationErrorClass string
	if err := st.DBForTest().QueryRowContext(ctx, `
SELECT COALESCE(error_class, '') FROM runtime_generations WHERE generation_id = ?`, allocation.GenerationID).Scan(&generationErrorClass); err != nil {
		t.Fatalf("query generation error class: %v", err)
	}
	if generationErrorClass == sandboxOOMCause {
		t.Fatal("undiagnosed death must not be recorded as an OOM")
	}
}

// A held generation (host processes survived) must not get a notice: nothing was
// repaired, the session is still locked, and telling the user the work was
// interrupted while the sandbox may still be running would be wrong.
func TestHeldGenerationGetsNoNotice(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, owner := openServerOwnedStore(t, ctx, dir)
	now := time.Now().UTC()
	session := createServerTestSession(t, ctx, st, dir, "sess_held_no_notice", string(sessionstate.RunningIdle), now.Add(-2*time.Minute), nil)
	cfg := testServerConfig(dir)
	allocation, err := st.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     store.GenerationLeaseOwner(owner.UUID),
		LeaseTTL:  time.Minute,
		Now:       now.Add(-3 * time.Minute),
		Config:    serverTestAllocatorConfig(cfg, session.Agent),
	})
	if err != nil {
		t.Fatalf("allocate generation: %v", err)
	}
	if err := st.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, now.Add(-3*time.Minute+time.Second)); err != nil {
		t.Fatalf("mark generation live: %v", err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `
UPDATE runtime_generations
SET status = 'idle',
    lease_owner = ?,
    lease_expires_at = ?
WHERE generation_id = ?`, store.GenerationLeaseOwner("previous-owner"), now.Add(-time.Minute).Format(time.RFC3339Nano), allocation.GenerationID); err != nil {
		t.Fatalf("expire generation: %v", err)
	}
	srv := &Server{
		cfg:     cfg,
		store:   st,
		runtime: &survivingRemnantRuntime{recordingRuntime: recordingRuntime{destroyRuntimeErr: errors.New("runsc delete failed")}},
		watcher: newServerTestWatcher(t, filepath.Join(dir, "sessions"), st, events.NewHub()),
		hub:     events.NewHub(),
		log:     slog.Default(),
	}
	srv.SetOwnerUUID(owner.UUID)

	if _, err := srv.RecoverExpiredRuntimeResources(ctx, now); err != nil {
		t.Fatalf("recover expired runtime resources: %v", err)
	}
	messages, err := st.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "已丢失") {
			t.Fatalf("held generation must not produce an interruption notice, got %q", message.Content)
		}
	}
}

func TestSandboxDeathNoticeNamesTheLimit(t *testing.T) {
	notice := sandboxDeathNotice(sandboxOOMCause, 4<<30)
	if !strings.Contains(notice, "4.0 GiB") {
		t.Fatalf("notice should quote the configured limit, got %q", notice)
	}
	// With no limit configured the sentence must still read correctly.
	if got := sandboxDeathNotice(sandboxOOMCause, 0); strings.Contains(got, "GiB") {
		t.Fatalf("unknown limit must not fabricate a number, got %q", got)
	}
}
