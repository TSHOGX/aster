package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// Automatic continuation of a turn whose model call died on a transient upstream
// fault.
//
// Why this exists: the upstream enforces a per-minute token budget. When it is
// exhausted mid-turn the model call fails, the turn is marked terminal, and the
// user is left with a reply that stops mid-sentence — usually right after the
// agent said it was about to run a tool. The sandbox and its pi session are still
// healthy and still hold the full conversation, so the work can be continued in
// place.
//
// Why continuation and not a retry: the failed turn may already have executed
// tool calls with side effects (files written, data loaded). Re-dispatching it
// would repeat them. Appending a new turn instead asks the agent to carry on from
// where it stopped, which it can do because its own session state survived. This
// is the same reasoning behind requeueExpiredLeasedTurnsTx refusing to requeue a
// turn that already began executing.
//
// Scope: only error classes known to be transient, only while the sandbox is
// alive, only when nothing else is in flight, and only a bounded number of times
// per user turn. Everything else stays failed for a human to look at.

// resumableErrorClasses are the turn failure classes worth continuing. Each is a
// fault on the path to the model rather than in the work the agent was doing, so
// the agent's own state is intact and continuing is safe.
//
// Deliberately excludes agent_prompt_rejected (the pi session history is already
// malformed, so a continuation hits the same rejection) and session_destroyed /
// sandbox_heartbeat_stalled_manual (nothing left to continue into).
var resumableErrorClasses = map[string]bool{
	"upstream_rate_limit": true,
	"proxy_context_lost":  true,
	"upstream_timeout":    true,
	"upstream_5xx":        true,
}

// resumeContinuationPrompt is the continuation turn's content. Phrased to resume
// rather than restart: the agent still holds its full context, and redoing
// finished work would waste the budget that just ran out and could repeat side
// effects.
const resumeContinuationPrompt = "上一次回复因模型服务临时故障中断了。请从中断的地方继续，不要重复已经完成的工作。"

// maybeResumeFailedTurn continues a turn that just failed on a transient upstream
// fault. Best-effort by design: every declined path logs at debug and returns,
// because not resuming is a normal outcome and must never disturb the turn
// completion path that invoked it.
func (s *Server) maybeResumeFailedTurn(ctx context.Context, envelope bridge.Envelope) {
	resume := s.cfg.Harness.Bridge.TurnResume
	if !resume.Enabled || envelope.TurnID == nil {
		return
	}
	status, errorClass, ok := ackCompletionOutcome(envelope)
	if !ok || status != "failed" {
		return
	}
	if !resumableErrorClasses[errorClass] {
		s.log.Debug("turn failure is not resumable",
			"session_id", envelope.SessionID, "turn_id", *envelope.TurnID, "error_class", errorClass)
		return
	}
	// Detached from the caller's context on purpose: that context is scoped to
	// processing this one envelope and is cancelled as soon as it returns, which
	// would cut the backoff wait short.
	go s.resumeFailedTurnAfterBackoff(envelope.SessionID, *envelope.TurnID, errorClass, resume)
}

// resumeFailedTurnAfterBackoff waits out the backoff, then appends the
// continuation turn. The wait lets a per-minute upstream budget refill;
// continuing immediately would fail again on the same exhausted window.
func (s *Server) resumeFailedTurnAfterBackoff(sessionID string, turnID int64, errorClass string, resume config.TurnResumeConfig) {
	if backoff := resume.Backoff.Duration; backoff > 0 {
		time.Sleep(backoff)
	}
	// Bounded on its own, not inherited: this runs after the originating request
	// is long gone. If the orchestrator restarts during the wait the resume simply
	// never happens, and the turn stays failed — startup recovery's view of the
	// session is unaffected because nothing was written yet.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := s.store.ResumeFailedTurn(ctx, store.ResumeFailedTurnParams{
		SessionID:   sessionID,
		TurnID:      turnID,
		Content:     resumeContinuationPrompt,
		MaxAttempts: resume.MaxAttempts,
		Now:         time.Now().UTC(),
	})
	if errors.Is(err, store.ErrTurnNotResumable) {
		// Expected: the user already followed up, the budget is spent, or the
		// sandbox went away while we waited.
		s.log.Debug("declined to resume turn",
			"session_id", sessionID, "turn_id", turnID, "error_class", errorClass)
		return
	}
	if err != nil {
		s.log.Warn("failed to resume turn",
			"session_id", sessionID, "turn_id", turnID, "error_class", errorClass, "error", err)
		return
	}
	s.log.Info("resumed failed turn",
		"session_id", sessionID,
		"failed_turn_id", turnID,
		"resume_turn_id", result.TurnID,
		"attempt", result.Attempt,
		"error_class", errorClass)
	// Tell watchers the session is working again, so the UI shows activity rather
	// than looking idle until the first token arrives.
	s.hub.Publish(events.Event{
		Type:      "session." + string(sessionstate.RunningActive),
		SessionID: sessionID,
	})
}

// ackCompletionOutcome reads the terminal status and error class off an
// ack_turn_completed envelope. ok=false means the payload did not parse, in which
// case the caller leaves the turn alone.
func ackCompletionOutcome(envelope bridge.Envelope) (status string, errorClass string, ok bool) {
	var payload struct {
		Status     string `json:"status"`
		ErrorClass string `json:"error_class"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return "", "", false
	}
	status = strings.TrimSpace(payload.Status)
	if status == "" {
		// The bridge defaults an absent status to "completed" before persisting,
		// so a missing status is never a failure.
		status = "completed"
	}
	return status, strings.TrimSpace(payload.ErrorClass), true
}
