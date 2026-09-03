package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func (s *Server) runScheduler(ctx context.Context) {
	interval := s.cfg.Harness.Scheduler.PollInterval.Duration
	if interval <= 0 {
		interval = 5 * time.Second
	}
	workers := s.cfg.Harness.Scheduler.WorkerCount
	if workers <= 0 {
		workers = 1
	}
	if cap := s.cfg.Harness.Scheduler.MaxConcurrentRuns; cap > 0 && workers > cap {
		workers = cap
	}
	jobs := make(chan store.ScheduleRun, workers*2)
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case run := <-jobs:
					s.executeScheduleRun(ctx, run)
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	enqueue := func(run store.ScheduleRun) bool {
		s.schedulerRunsMu.Lock()
		_, active := s.schedulerRunsActive[run.ID]
		if !active {
			s.schedulerRunsActive[run.ID] = struct{}{}
		}
		s.schedulerRunsMu.Unlock()
		if active {
			return true
		}
		select {
		case jobs <- run:
			return true
		default:
			s.schedulerRunsMu.Lock()
			delete(s.schedulerRunsActive, run.ID)
			s.schedulerRunsMu.Unlock()
			return false
		}
	}
	dispatch := func() {
		// A process restart can happen after StartScheduleRun but before the
		// worker records the terminal state. Re-queue those rows first so a
		// completed turn is reconciled instead of remaining "running" forever.
		if running, err := s.store.ListRunningScheduleRuns(ctx, workers*2); err == nil {
			for _, r := range running {
				if !enqueue(r) {
					return
				}
			}
		} else if s.log != nil {
			s.log.Warn("schedule running recovery list failed", "error", err)
		}
		queued, err := s.store.ListQueuedScheduleRuns(ctx, time.Now().UTC(), workers*2)
		if err == nil {
			for _, r := range queued {
				if !enqueue(r) {
					return
				}
			}
		}
		if !s.cfg.Harness.Scheduler.Enabled || !s.store.FeatureEnabled(ctx, "cron_agent") {
			return
		}
		due, err := s.store.ListDueSchedules(ctx, time.Now().UTC(), workers*2)
		if err != nil {
			return
		}
		for _, sc := range due {
			if sc.NextRunAt == nil {
				continue
			}
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			spec, err := parser.Parse(sc.CronExpr)
			if err != nil {
				continue
			}
			scheduled := sc.NextRunAt.UTC()
			next := spec.Next(scheduled.In(mustLocation(sc.Timezone))).UTC()
			run := store.ScheduleRun{ID: newID("srun"), ScheduleID: sc.ID, UserID: sc.UserID, ScheduledAt: scheduled, Status: "queued", Attempt: 0, CreatedAt: time.Now().UTC()}
			if err := s.store.ClaimScheduleDue(ctx, sc.ID, sc.Version, next, scheduled, run); err == nil {
				if s.log != nil {
					s.log.Info("cron schedule claimed", "schedule_id", sc.ID, "run_id", run.ID, "scheduled_at", scheduled)
				}
				if !enqueue(run) {
					return
				}
			}
		}
	}
	dispatch()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			dispatch()
		case <-s.schedulerWake:
			dispatch()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) executeScheduleRun(parent context.Context, run store.ScheduleRun) {
	defer func() { s.schedulerRunsMu.Lock(); delete(s.schedulerRunsActive, run.ID); s.schedulerRunsMu.Unlock() }()
	sched, err := s.store.GetSchedule(parent, run.ScheduleID)
	if err != nil {
		if s.log != nil {
			s.log.Warn("schedule run skipped: schedule missing", "run_id", run.ID, "schedule_id", run.ScheduleID, "error", err)
		}
		return
	}
	if sched.Archived {
		_ = s.store.FinishScheduleRun(parent, run.ID, "canceled", "archived", "schedule was archived before execution", "schedule was archived before execution", time.Now().UTC())
		return
	}
	if run.Status == "running" {
		s.resumeScheduleRun(parent, run, sched)
		return
	}
	timeout := s.scheduleTimeout(sched)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	now := time.Now().UTC()
	var session store.Session
	if sched.ResultMode == "existing_session" {
		if sched.TargetSessionID == "" {
			s.finishScheduleFailure(parent, run, sched, "target_session", "target session is not configured")
			return
		}
		var err error
		session, err = s.store.GetSession(ctx, sched.TargetSessionID)
		if errors.Is(err, sql.ErrNoRows) || session.UserID != run.UserID || session.Mode != "agent" || session.Status == string(sessionstate.Destroyed) {
			s.finishScheduleFailure(parent, run, sched, "target_session", "target session is unavailable")
			return
		}
		if err != nil {
			s.finishScheduleFailure(parent, run, sched, "target_session", err.Error())
			return
		}
	} else {
		id := newID("sess")
		session = store.Session{ID: id, UserID: run.UserID, Status: string(sessionstate.Created), Agent: "pi", Mode: "agent", Workspace: filepath.Join(s.cfg.SessionsRoot, id), RestoreID: "unused-" + id, AutoCheckpointEnabled: true, CreatedAt: now, UpdatedAt: now}
		if s.cfg.SessionRetention > 0 {
			expires := now.Add(s.cfg.SessionRetention)
			session.ExpiresAt = &expires
		}
		if err := s.store.CreateSession(ctx, session); err != nil {
			if s.log != nil {
				s.log.Error("schedule run session creation failed", "run_id", run.ID, "error", err)
			}
			s.finishScheduleFailure(parent, run, sched, "session_create", err.Error())
			return
		}
		title := fmt.Sprintf("%s · %s", sched.Name, run.ScheduledAt.In(mustLocation(sched.Timezone)).Format("2006-01-02 15:04"))
		_ = s.store.UpdateSessionTitle(ctx, session.ID, title, true)
		session.Title = title
		s.hub.Publish(events.Event{Type: "session.created", SessionID: session.ID, Payload: publicSession(session)})
	}
	id := session.ID
	owner := store.GenerationLeaseOwner(s.ownerUUID)
	ensured, err := s.ensureActiveGeneration(ctx, session, owner)
	if err == nil {
		err = s.startEnsuredGeneration(ctx, session, ensured, startFailureInputAcceptable)
	}
	if err != nil {
		if s.log != nil {
			s.log.Error("schedule run generation start failed", "run_id", run.ID, "error", err)
		}
		s.finishScheduleFailure(parent, run, sched, "generation_start", err.Error())
		return
	}
	enq, err := s.store.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: id, Content: sched.Prompt, Now: time.Now().UTC()})
	if err != nil {
		if s.log != nil {
			s.log.Error("schedule run enqueue failed", "run_id", run.ID, "error", err)
		}
		s.finishScheduleFailure(parent, run, sched, "enqueue", err.Error())
		return
	}
	_ = s.store.StartScheduleRun(parent, run.ID, id, enq.TurnID, time.Now().UTC())
	s.hub.Publish(events.Event{Type: "message.created", SessionID: id, Payload: enq.Message})
	for {
		select {
		case <-ctx.Done():
			if s.log != nil {
				s.log.Warn("schedule run timed out", "run_id", run.ID, "timeout", timeout)
			}
			s.finishScheduleFailureStatus(parent, run, sched, "timed_out", "timeout", ctx.Err().Error())
			return
		case <-time.After(500 * time.Millisecond):
			status, turnErr, e := s.store.TurnStatus(parent, id, enq.TurnID)
			if e != nil {
				continue
			}
			if status == "completed" {
				if s.log != nil {
					s.log.Info("schedule run completed", "run_id", run.ID, "session_id", id)
				}
				summary, _ := s.store.LatestAssistantMessage(parent, id)
				_ = s.store.FinishScheduleRun(parent, run.ID, "succeeded", "", "", truncateScheduleOutput(summary), time.Now().UTC())
				if sched.ResultMode != "existing_session" {
					if latest, loadErr := s.store.GetSession(parent, id); loadErr == nil && latest.Status == string(sessionstate.RunningIdle) && latest.ActiveGenerationID != "" {
						if releaseErr := s.store.ReleaseIdleGeneration(parent, id, latest.ActiveGenerationID, owner, time.Now().UTC()); releaseErr == nil {
							s.hub.Publish(events.Event{Type: "session.checkpointed", SessionID: id})
						}
					}
				}
				return
			}
			if status == "failed" || status == "canceled" {
				if s.log != nil {
					s.log.Warn("schedule run failed", "run_id", run.ID, "session_id", id, "error", turnErr)
				}
				s.finishScheduleFailure(parent, run, sched, "turn", turnErr)
				return
			}
		}
	}
}

// resumeScheduleRun reconciles a schedule run that was already started by an
// earlier scheduler process. The run row carries the exact session and turn
// identifiers, so recovery can observe that turn directly without creating a
// duplicate session or enqueueing a second prompt.
func (s *Server) resumeScheduleRun(parent context.Context, run store.ScheduleRun, sched store.AgentSchedule) {
	if run.SessionID == "" || run.TurnID == nil {
		s.finishScheduleFailureStatus(parent, run, sched, "failed", "recovery", "running schedule run has no session or turn")
		return
	}
	timeout := s.scheduleTimeout(sched)
	started := run.StartedAt
	if started == nil || started.IsZero() {
		started = &run.CreatedAt
	}
	remaining := timeout
	if started != nil && !started.IsZero() {
		remaining = timeout - time.Since(*started)
	}
	// Reconcile the durable turn before applying the timeout. A scheduler that
	// was restarted days later must still report a turn that already completed
	// as succeeded; otherwise recovery would rewrite a successful run as a
	// timeout merely because its worker was offline.
	status, turnErr, err := s.store.TurnStatus(parent, run.SessionID, *run.TurnID)
	if err == nil {
		switch status {
		case "completed":
			if s.log != nil {
				s.log.Info("schedule run recovered as completed", "run_id", run.ID, "session_id", run.SessionID)
			}
			s.finishScheduleSuccess(parent, run, sched)
			return
		case "failed", "canceled":
			s.finishScheduleFailure(parent, run, sched, "turn", turnErr)
			return
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		s.finishScheduleFailureStatus(parent, run, sched, "failed", "recovery", "schedule run turn no longer exists")
		return
	}
	if remaining <= 0 {
		s.finishScheduleFailureStatus(parent, run, sched, "timed_out", "timeout", "schedule run timed out during scheduler recovery")
		return
	}
	ctx, cancel := context.WithTimeout(parent, remaining)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			s.finishScheduleFailureStatus(parent, run, sched, "timed_out", "timeout", ctx.Err().Error())
			return
		case <-time.After(500 * time.Millisecond):
			status, turnErr, err := s.store.TurnStatus(parent, run.SessionID, *run.TurnID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					s.finishScheduleFailureStatus(parent, run, sched, "failed", "recovery", "schedule run turn no longer exists")
					return
				}
				continue
			}
			switch status {
			case "completed":
				if s.log != nil {
					s.log.Info("schedule run recovered as completed", "run_id", run.ID, "session_id", run.SessionID)
				}
				s.finishScheduleSuccess(parent, run, sched)
				return
			case "failed", "canceled":
				s.finishScheduleFailure(parent, run, sched, "turn", turnErr)
				return
			}
		}
	}
}

func (s *Server) scheduleTimeout(sched store.AgentSchedule) time.Duration {
	timeout := time.Duration(sched.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = s.cfg.Harness.Scheduler.DefaultTimeout.Duration
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return timeout
}

func (s *Server) finishScheduleSuccess(parent context.Context, run store.ScheduleRun, sched store.AgentSchedule) {
	// A recovered run may have later turns in the same existing session. The
	// messages table does not carry a turn foreign key, so selecting the latest
	// assistant message here could attach a newer, unrelated answer to this run.
	// Keep the durable status authoritative and leave the summary empty rather
	// than display misleading output.
	if err := s.store.FinishScheduleRun(parent, run.ID, "succeeded", "", "", "", time.Now().UTC()); err != nil {
		if s.log != nil {
			s.log.Warn("schedule run completion update failed", "run_id", run.ID, "error", err)
		}
		return
	}
	if sched.ResultMode != "existing_session" {
		latest, loadErr := s.store.GetSession(parent, run.SessionID)
		if loadErr != nil || latest.Status != string(sessionstate.RunningIdle) || latest.ActiveGenerationID == "" {
			return
		}
		owner := store.GenerationLeaseOwner(s.ownerUUID)
		if releaseErr := s.store.ReleaseIdleGeneration(parent, run.SessionID, latest.ActiveGenerationID, owner, time.Now().UTC()); releaseErr == nil && s.hub != nil {
			s.hub.Publish(events.Event{Type: "session.checkpointed", SessionID: run.SessionID})
		}
	}
}

func (s *Server) finishScheduleFailure(ctx context.Context, run store.ScheduleRun, sched store.AgentSchedule, class, message string) {
	s.finishScheduleFailureStatus(ctx, run, sched, "failed", class, message)
}

func (s *Server) finishScheduleFailureStatus(ctx context.Context, run store.ScheduleRun, sched store.AgentSchedule, status, class, message string) {
	_ = s.store.FinishScheduleRun(ctx, run.ID, status, class, message, truncateScheduleOutput(message), time.Now().UTC())
	if run.Attempt >= sched.MaxRetries {
		return
	}
	backoff := time.Duration(sched.RetryBackoffSeconds) * time.Second
	if backoff <= 0 {
		backoff = s.cfg.Harness.Scheduler.RetryBackoff.Duration
	}
	if backoff < 0 {
		backoff = 0
	}
	now := time.Now().UTC()
	retry := store.ScheduleRun{ID: newID("srun"), ScheduleID: sched.ID, UserID: sched.UserID, ScheduledAt: now.Add(backoff), Status: "queued", Attempt: run.Attempt + 1, CreatedAt: now}
	if err := s.store.CreateScheduleRun(ctx, retry); err == nil {
		select {
		case s.schedulerWake <- struct{}{}:
		default:
		}
	}
}

func truncateScheduleOutput(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 4000 {
		return v[:4000]
	}
	return v
}
