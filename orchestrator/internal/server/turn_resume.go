package server

import (
	"context"
	"errors"
	"time"

	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// A continuation is a new bounded turn, never re-execution of prior tool effects.
const resumeContinuationPrompt = "上一次回复因模型服务临时故障中断了。请从中断的地方继续，不要重复已经完成的工作。"

// Failed turns themselves are durable intent. One maintenance worker discovers
// eligible continuations after backoff, including after an application restart.
func (s *Server) resumeDueTurns(ctx context.Context) error {
	configuration := s.cfg.Harness.Bridge.TurnResume
	if !configuration.Enabled {
		return nil
	}
	candidates, err := s.store.PendingTurnContinuations(ctx, s.continuationAfter, time.Now().UTC().Add(-configuration.Backoff.Duration), 32)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		s.continuationAfter = ""
	}
	var failures []error
	for _, candidate := range candidates {
		s.continuationAfter = candidate.SessionID
		result, err := s.store.ResumeFailedTurn(ctx, store.ResumeFailedTurnParams{SessionID: candidate.SessionID, TurnID: candidate.TurnID, Content: resumeContinuationPrompt, MaxAttempts: configuration.MaxAttempts, Now: time.Now().UTC()})
		if errors.Is(err, store.ErrTurnNotResumable) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		s.log.Info("continued failed turn", "session_id", candidate.SessionID, "failed_turn_id", candidate.TurnID, "continuation_turn_id", result.TurnID, "attempt", result.Attempt)
		s.hub.Publish(events.Event{Type: "session." + string(sessionstate.RunningActive), SessionID: candidate.SessionID})
	}
	return errors.Join(failures...)
}
