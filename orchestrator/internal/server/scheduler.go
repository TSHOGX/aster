package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/deploymentgate"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func parseScheduleSpec(expr string) (cron.Schedule, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	return parser.Parse(expr)
}

func (s *Server) misfireGrace() time.Duration {
	grace := s.cfg.Harness.Scheduler.MisfireGrace.Duration
	if grace < 0 {
		return 0
	}
	return grace
}

func (s *Server) maxPendingRuns() int {
	limit := s.cfg.Harness.Scheduler.MaxPendingRunsPerSchedule
	if limit <= 0 {
		return 3
	}
	return limit
}

// schedulePlan is what one due schedule should do on this tick.
type schedulePlan struct {
	// Fire reports whether a run should be claimed for ScheduledFor.
	Fire bool
	// ScheduledFor is the window the run belongs to (only set when Fire).
	ScheduledFor time.Time
	// Next is the cursor to store in next_run_at. Always strictly after now, so a
	// tick can never leave the schedule immediately due again.
	Next time.Time
	// SkippedWindows counts windows abandoned without running, for the log.
	SkippedWindows int
}

// planScheduleTick decides what to do with a schedule whose next_run_at has
// passed, and is the whole of the misfire policy.
//
// The previous behaviour advanced one window per tick (next := spec.Next(missed)),
// which replayed history in order: after a day of downtime a */1 schedule owed 1447
// windows, each a cold sandbox plus a real model call. Because a run costs minutes
// and a window arrives every minute, the backlog grew faster than it drained — the
// measured 36 -> 47 in ten minutes — so it could never converge.
//
// The policy: a window still within grace fires (a restart straddling a minute
// boundary should not silently lose that minute); anything older is abandoned and
// the cursor jumps to the first trigger after now. At most one run is owed per
// tick, so downtime of a minute and downtime of a month cost the same.
//
// Next is always computed from now, never from the missed window, which is the
// invariant that ends the walk.
func planScheduleTick(spec cron.Schedule, sc store.AgentSchedule, now time.Time, grace time.Duration) schedulePlan {
	if sc.NextRunAt == nil {
		return schedulePlan{Next: spec.Next(now).UTC()}
	}
	missed := sc.NextRunAt.UTC()
	lateness := now.Sub(missed)

	// Within grace (including a window due exactly now, or a clock that stepped
	// back): honour it and advance from the window itself, keeping the cadence
	// aligned when nothing was actually missed.
	if lateness <= grace {
		next := spec.Next(missed.In(mustLocation(sc.Timezone))).UTC()
		if !next.After(now) {
			next = spec.Next(now.In(mustLocation(sc.Timezone))).UTC()
		}
		return schedulePlan{Fire: true, ScheduledFor: missed, Next: next}
	}

	// Too late. Count what is being abandoned for the operator's benefit, then
	// resume from the first trigger after now.
	skipped := 0
	for cursor := missed; cursor.Before(now) && skipped < 100000; skipped++ {
		cursor = spec.Next(cursor.In(mustLocation(sc.Timezone))).UTC()
	}
	return schedulePlan{Next: spec.Next(now.In(mustLocation(sc.Timezone))).UTC(), SkippedWindows: skipped}
}

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
	// The queue carries run ids, not run rows. A row copied into the channel is a
	// snapshot that stops tracking the database the moment it is queued, which is
	// how disabling a schedule failed to stop work already in flight: the worker
	// acted on a stale copy that still looked runnable. Passing the id forces the
	// worker to re-read authoritative state at the moment it starts, so the
	// database stays the single source of truth for "should this still run".
	jobs := make(chan string, workers*2)
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case runID := <-jobs:
					s.startScheduleRunFromQueue(ctx, runID)
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	enqueue := func(runID string) bool {
		s.schedulerRunsMu.Lock()
		_, active := s.schedulerRunsActive[runID]
		if !active {
			s.schedulerRunsActive[runID] = struct{}{}
		}
		s.schedulerRunsMu.Unlock()
		if active {
			return true
		}
		select {
		case jobs <- runID:
			return true
		default:
			s.schedulerRunsMu.Lock()
			delete(s.schedulerRunsActive, runID)
			s.schedulerRunsMu.Unlock()
			return false
		}
	}
	dispatch := func() {
		if deploymentgate.BackgroundHeld() {
			return
		}
		// A process restart can happen after StartScheduleRun but before the
		// worker records the terminal state. Re-queue those rows first so a
		// completed turn is reconciled instead of remaining "running" forever.
		if running, err := s.store.ListRunningScheduleRuns(ctx, workers*2); err == nil {
			for _, r := range running {
				if !enqueue(r.ID) {
					return
				}
			}
		} else if s.log != nil {
			s.log.Warn("schedule running recovery list failed", "error", err)
		}
		queued, err := s.store.ListQueuedScheduleRuns(ctx, time.Now().UTC(), workers*2)
		if err == nil {
			for _, r := range queued {
				if !enqueue(r.ID) {
					return
				}
			}
		}
		// Scheduling is controlled by the scheduler config. The feature flag is
		// an admin UI capability gate and must not silently disable already
		// persisted user schedules (manual runs and cron runs share the same
		// durable schedule state).
		if deploymentgate.Draining() || !s.cfg.Harness.Scheduler.Enabled {
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
			spec, err := parseScheduleSpec(sc.CronExpr)
			if err != nil {
				continue
			}
			now := time.Now().UTC()
			plan := planScheduleTick(spec, sc, now, s.misfireGrace())
			if plan.SkippedWindows > 0 && s.log != nil {
				s.log.Warn("cron misfire windows skipped",
					"schedule_id", sc.ID,
					"missed_window_at", sc.NextRunAt.UTC(),
					"skipped_windows", plan.SkippedWindows,
					"resumed_at", plan.Next,
					"misfire_grace", s.misfireGrace())
			}
			if !plan.Fire {
				// Nothing to run for the stale window; just move the cursor
				// forward. No run row is written: recording a skip per missed
				// minute would move the backlog from the queue into the table.
				if err := s.store.AdvanceSchedule(ctx, sc.ID, sc.Version, plan.Next, now); err != nil && s.log != nil {
					s.log.Warn("cron misfire skip advance failed", "schedule_id", sc.ID, "error", err)
				}
				continue
			}
			if pending, err := s.store.CountPendingScheduleRuns(ctx, sc.ID); err == nil && pending >= s.maxPendingRuns() {
				// The backlog ceiling is reached. Advance past this window rather
				// than claiming a run that would queue behind runs already waiting.
				if s.log != nil {
					s.log.Warn("cron schedule window dropped: pending backlog at limit",
						"schedule_id", sc.ID, "pending", pending, "limit", s.maxPendingRuns(), "window_at", plan.ScheduledFor)
				}
				if err := s.store.AdvanceSchedule(ctx, sc.ID, sc.Version, plan.Next, now); err != nil && s.log != nil {
					s.log.Warn("cron backlog advance failed", "schedule_id", sc.ID, "error", err)
				}
				continue
			}
			run := store.ScheduleRun{ID: newID("srun"), ScheduleID: sc.ID, UserID: sc.UserID, ScheduledAt: plan.ScheduledFor, Status: "queued", Attempt: 0, CreatedAt: time.Now().UTC()}
			if err := s.store.ClaimScheduleDue(ctx, sc.ID, sc.Version, plan.Next, plan.ScheduledFor, run); err == nil {
				if s.log != nil {
					s.log.Info("cron schedule claimed", "schedule_id", sc.ID, "run_id", run.ID, "scheduled_at", plan.ScheduledFor, "next_run_at", plan.Next)
				}
				if !enqueue(run.ID) {
					return
				}
			}
		}
	}
	s.recoverStaleScheduleCursors(ctx)
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

// startScheduleRunFromQueue re-reads a run and its schedule before any work
// begins, and is the single admission gate for the worker pool.
//
// Everything it checks was previously trusted from the queued snapshot, which is
// why switching a schedule off did not stop runs already handed to a worker: the
// only enabled check happened when the row was selected, minutes earlier. The
// authoritative read has to happen here, in the worker, because the gap between
// dispatch and execution is exactly where a user's decision lands.
// recoverStaleScheduleCursors re-anchors next_run_at for schedules left behind by
// downtime, once, before the poll loop starts.
//
// The dispatcher alone would eventually converge, but it would do so by walking:
// every stale schedule looks due on the first tick, and each tick advances one
// window. Recomputing the cursor up front makes downtime cost a single write per
// schedule instead of a tick-by-tick crawl through history, and means the first
// dispatch sees an accurate picture.
//
// Windows inside the misfire grace are left alone: those are still legitimately
// runnable and the dispatcher should claim them normally.
func (s *Server) recoverStaleScheduleCursors(ctx context.Context) {
	if !s.cfg.Harness.Scheduler.Enabled {
		return
	}
	now := time.Now().UTC()
	grace := s.misfireGrace()
	stale, err := s.store.ListStaleSchedules(ctx, now.Add(-grace), 0)
	if err != nil {
		if s.log != nil {
			s.log.Warn("schedule startup recovery list failed", "error", err)
		}
		return
	}
	for _, sc := range stale {
		spec, err := parseScheduleSpec(sc.CronExpr)
		if err != nil {
			continue
		}
		plan := planScheduleTick(spec, sc, now, grace)
		if plan.Fire || plan.SkippedWindows == 0 {
			continue
		}
		if err := s.store.AdvanceSchedule(ctx, sc.ID, sc.Version, plan.Next, now); err != nil {
			if s.log != nil {
				s.log.Warn("schedule startup recovery advance failed", "schedule_id", sc.ID, "error", err)
			}
			continue
		}
		if s.log != nil {
			s.log.Warn("cron cursor re-anchored after downtime",
				"schedule_id", sc.ID,
				"stale_next_run_at", sc.NextRunAt,
				"skipped_windows", plan.SkippedWindows,
				"resumed_at", plan.Next)
		}
	}
}

func (s *Server) startScheduleRunFromQueue(ctx context.Context, runID string) {
	release := func() {
		s.schedulerRunsMu.Lock()
		delete(s.schedulerRunsActive, runID)
		s.schedulerRunsMu.Unlock()
	}
	run, err := s.store.GetScheduleRunByID(ctx, runID)
	if err != nil {
		if s.log != nil {
			s.log.Warn("schedule run dropped: run row unreadable", "run_id", runID, "error", err)
		}
		release()
		return
	}
	// A run canceled while it sat in the queue must not start. Its terminal state
	// is already recorded, so there is nothing to write here.
	if run.Status != "queued" && run.Status != "running" {
		if s.log != nil {
			s.log.Info("schedule run dropped: no longer pending", "run_id", runID, "status", run.Status)
		}
		release()
		return
	}
	sched, err := s.store.GetSchedule(ctx, run.ScheduleID)
	if err != nil {
		if s.log != nil {
			s.log.Warn("schedule run dropped: schedule unreadable", "run_id", runID, "schedule_id", run.ScheduleID, "error", err)
		}
		release()
		return
	}
	// Disabled and archived both mean "do not start new work". A run already
	// 'running' is left to converge on its own turn: its prompt is in flight, and
	// finishing the bookkeeping is more honest than abandoning the row.
	if run.Status == "queued" && (!sched.Enabled || sched.Archived) {
		reason := "schedule was disabled before execution"
		class := "schedule_disabled"
		if sched.Archived {
			reason = "schedule was archived before execution"
			class = "archived"
		}
		if err := s.store.FinishScheduleRun(ctx, run.ID, "canceled", class, reason, reason, time.Now().UTC()); err != nil && !errors.Is(err, store.ErrScheduleRunNotOpen) && s.log != nil {
			s.log.Warn("schedule run cancel write failed", "run_id", run.ID, "error", err)
		}
		if s.log != nil {
			s.log.Info("schedule run canceled before start", "run_id", run.ID, "schedule_id", sched.ID, "enabled", sched.Enabled, "archived", sched.Archived)
		}
		release()
		return
	}
	if deploymentgate.BackgroundHeld() {
		return
	}
	s.executeScheduleRun(ctx, run)
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
	// Take the run before spending anything on it. A cancellation that lands while
	// this run waited in the queue wins here, before a session exists, a sandbox
	// cold-starts, or a prompt reaches the agent.
	if err := s.store.ClaimScheduleRunStart(parent, run.ID, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrScheduleRunNotOpen) {
			if s.log != nil {
				s.log.Info("schedule run dropped: claim lost", "run_id", run.ID, "schedule_id", sched.ID)
			}
			return
		}
		if s.log != nil {
			s.log.Warn("schedule run claim failed", "run_id", run.ID, "error", err)
		}
		return
	}
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
		session.ExecutionBackend = s.newSessionBackend
		if s.newSessionBackend == "khora" {
			session.Workspace = "workspace:" + id
			session.AutoCheckpointEnabled = false
		}
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
		if isScheduleBusyError(err) {
			s.finishScheduleFailureStatus(parent, run, sched, "skipped_busy", "session_busy", "skipped because the previous conversation is still running")
		} else {
			s.finishScheduleFailure(parent, run, sched, "generation_start", err.Error())
		}
		return
	}
	enq, err := s.store.EnqueueTurnMessage(ctx, store.EnqueueTurnMessageParams{SessionID: id, Content: sched.Prompt, Now: time.Now().UTC()})
	if err != nil {
		if s.log != nil {
			s.log.Error("schedule run enqueue failed", "run_id", run.ID, "error", err)
		}
		if isScheduleBusyError(err) {
			// The target session is busy with an earlier turn. This is a
			// deliberate skip, not an execution failure.
			s.finishScheduleFailureStatus(parent, run, sched, "skipped_busy", "session_busy", "skipped because the previous conversation is still running")
			return
		}
		s.finishScheduleFailure(parent, run, sched, "enqueue", err.Error())
		return
	}
	if err := s.store.AttachScheduleRunTurn(parent, run.ID, id, enq.TurnID, time.Now().UTC()); err != nil {
		// The run left the running state while the turn was being enqueued (a
		// cancel racing this claim). Withdraw the queued turn so the agent is not
		// left holding a prompt for a run nobody is waiting on.
		if errors.Is(err, store.ErrScheduleRunNotOpen) {
			if _, cancelErr := s.store.CancelQueuedTurn(parent, store.CancelQueuedTurnParams{SessionID: id, TurnID: enq.TurnID, Now: time.Now().UTC()}); cancelErr != nil && s.log != nil {
				s.log.Warn("schedule run turn withdrawal failed", "run_id", run.ID, "session_id", id, "turn_id", enq.TurnID, "error", cancelErr)
			}
			if s.log != nil {
				s.log.Info("schedule run abandoned after cancel", "run_id", run.ID, "session_id", id)
			}
			return
		}
		if s.log != nil {
			s.log.Warn("schedule run turn attach failed", "run_id", run.ID, "error", err)
		}
	}
	// The turn is queued; its transcript entry is published when the agent starts
	// it (see publishTurnUserMessage). Broadcast the queue change so a client
	// watching the target session sees the scheduled prompt waiting.
	s.publishSessionRuntime(id, enq.Runtime)
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
						if releaseErr := s.releaseScheduledIdle(parent, latest, owner); releaseErr != nil && s.log != nil {
							s.log.Warn("scheduled idle release pending", "session_id", id, "error", releaseErr)
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

func isScheduleBusyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "cannot accept input") ||
		strings.Contains(message, "generation lifecycle is busy") ||
		strings.Contains(message, "session is busy")
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
		if releaseErr := s.releaseScheduledIdle(parent, latest, owner); releaseErr != nil && s.log != nil {
			s.log.Warn("scheduled idle release pending", "session_id", run.SessionID, "error", releaseErr)
		}
	}
}

func (s *Server) finishScheduleFailure(ctx context.Context, run store.ScheduleRun, sched store.AgentSchedule, class, message string) {
	s.finishScheduleFailureStatus(ctx, run, sched, "failed", class, message)
}

func (s *Server) finishScheduleFailureStatus(ctx context.Context, run store.ScheduleRun, sched store.AgentSchedule, status, class, message string) {
	_ = s.store.FinishScheduleRun(ctx, run.ID, status, class, message, truncateScheduleOutput(message), time.Now().UTC())
	if status == "skipped_busy" {
		return
	}
	if deploymentgate.Paused() || run.Attempt >= sched.MaxRetries {
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
