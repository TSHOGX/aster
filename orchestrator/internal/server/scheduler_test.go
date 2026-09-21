package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/store"
)

func TestResumeScheduleRunReconcilesCompletedTurn(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC().Truncate(time.Second)
	sessionID := "sess_schedule_recovery"
	if err := st.CreateSession(ctx, store.Session{
		ID:        sessionID,
		UserID:    "alice",
		Status:    "created",
		Agent:     "pi",
		Mode:      "agent",
		Workspace: filepath.Join(t.TempDir(), "workspace"),

		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	enqueued, err := st.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: sessionID, Content: "run", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DBForTest().ExecContext(ctx, `UPDATE turns SET status='completed',completed_at=? WHERE id=?`, now, enqueued.TurnID); err != nil {
		t.Fatal(err)
	}

	schedule := store.AgentSchedule{ID: "sched_schedule_recovery", UserID: "alice", Name: "recovery", Prompt: "run", Enabled: true, Mode: "agent", ResultMode: "existing_session", TargetSessionID: sessionID, TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	started := now.Add(-time.Minute)
	if err := st.CreateScheduleRun(ctx, store.ScheduleRun{ID: "run_schedule_recovery", ScheduleID: schedule.ID, UserID: schedule.UserID, SessionID: sessionID, TurnID: &enqueued.TurnID, ScheduledAt: started, StartedAt: &started, Status: "running", CreatedAt: started}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true}, store: st, hub: events.NewHub()}
	srv.resumeScheduleRun(ctx, store.ScheduleRun{ID: "run_schedule_recovery", ScheduleID: schedule.ID, UserID: schedule.UserID, SessionID: sessionID, TurnID: &enqueued.TurnID, ScheduledAt: started, StartedAt: &started, Status: "running", CreatedAt: started}, schedule)

	run, err := st.GetScheduleRun(ctx, "run_schedule_recovery", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" || run.FinishedAt == nil {
		t.Fatalf("recovered run = %+v, want succeeded with finished_at", run)
	}
}
