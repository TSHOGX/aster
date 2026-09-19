package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// sendQueuedTurn is the ↑ button on a queue row: "send this one, now".
//
// One endpoint rather than two because the user's intent is the same either way,
// and which mechanism delivers it depends on state they cannot see and should not
// have to reason about:
//
//   - a turn is executing → steer, injecting the text into that turn so the work
//     already done survives;
//   - nothing is executing → release, handing the parked turn to the worker to
//     run as a turn of its own.
//
// Making the client choose would mean it had to hold a fresh projection to pick
// correctly, and would turn every race into a wrong answer instead of the server
// simply doing the right thing.
func (s *Server) sendQueuedTurn(w http.ResponseWriter, r *http.Request, sessionID string, turnID int64) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	// Shell sessions have no turn queue: their input goes straight to a pty, so
	// there is nothing parked to release and nothing to steer into.
	if session.Agent == string(agents.Shell) {
		backend, err := s.store.SessionExecutionBackend(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if backend != "khora" {
			writeError(w, http.StatusConflict, "session does not queue turns")
			return
		}
	}
	// The same states that accept a new prompt. `checkpointed` is included on
	// purpose: parked rows do not hold a sandbox open, so one can be reclaimed
	// underneath them, and releasing then has to wake the session exactly as a new
	// prompt would (see ensureRunnableGenerationHTTP).
	if !sessionstate.CanAcceptInput(session.Status) && session.Status != string(sessionstate.RunningActive) {
		writeError(w, http.StatusConflict, "session is "+session.Status)
		return
	}
	snapshot, err := s.store.GetSessionRuntimeSnapshot(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var target *store.TurnSnapshot
	for i := range snapshot.QueuedTurns {
		if snapshot.QueuedTurns[i].ID == turnID {
			target = &snapshot.QueuedTurns[i]
			break
		}
	}
	if target == nil {
		// Already claimed, cancelled, or steered: the row the user clicked is gone
		// from the queue. A conflict, not a fault — the client re-reads.
		writeErrorClass(w, http.StatusConflict, "turn_not_sendable", "turn is no longer queued")
		return
	}
	if snapshot.ActiveTurn == nil {
		s.releaseQueuedTurn(w, r, session, turnID)
		return
	}
	if session.Agent == string(agents.Shell) {
		writeError(w, http.StatusConflict, "shell turns cannot steer a running command")
		return
	}
	s.steerQueuedTurn(w, r, session, snapshot, *target)
}

// releaseQueuedTurn hands one parked turn to the worker.
//
// Only a parked row is releasable. A turn that is queued and un-parked is already
// on its way to the worker, so "send it" is a no-op the user cannot distinguish
// from success — reporting the conflict makes the client re-read and show the
// truth.
//
// The sandbox is ensured first, because a parked queue does not hold one open: it
// is excluded from the pending-turn predicate precisely so auto-checkpoint and
// idle-release can reclaim compute while the rows wait on the user. Releasing
// into a reclaimed session would otherwise flip auto_dispatch back on and leave
// the turn queued against nothing.
func (s *Server) releaseQueuedTurn(w http.ResponseWriter, r *http.Request, session store.Session, turnID int64) {
	if !s.ensureRunnableGenerationHTTP(w, r, session) {
		return
	}
	sessionID := session.ID
	runtime, err := s.store.ReleaseParkedTurn(r.Context(), store.ReleaseParkedTurnParams{
		SessionID: sessionID,
		TurnID:    turnID,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, store.ErrTurnNotReleasable) {
			writeErrorClass(w, http.StatusConflict, "turn_not_releasable", "turn is not parked")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishSessionRuntime(sessionID, runtime)
	// The worker picks it up on its own next claim against the generation ensured
	// above, so there is nothing to start here.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "released",
		"runtime": runtime,
	})
}

// steerQueuedTurn injects a queued row's text into the turn already running.
//
// Steering is why this is worth having at all: a correction reaches the agent
// without discarding what it has already done. Stop-and-resend is the same
// instruction at the cost of the whole turn.
func (s *Server) steerQueuedTurn(w http.ResponseWriter, r *http.Request, session store.Session, snapshot store.SessionRuntimeSnapshot, target store.TurnSnapshot) {
	commandID := ""
	backend, err := s.store.SessionExecutionBackend(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if backend == "khora" {
		commandID = fmt.Sprintf("control_steer_%s_%d", session.ActiveGenerationID, target.ID)
		commands, ok := s.executions.(interface {
			DeliverTurnCommand(context.Context, execution.TurnCommand) error
		})
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "execution control unavailable")
			return
		}
		if err := commands.DeliverTurnCommand(r.Context(), execution.TurnCommand{ID: fmt.Sprintf("control_steer_%s_%d", session.ActiveGenerationID, target.ID), SessionID: session.ID, GenerationID: session.ActiveGenerationID, TurnID: snapshot.ActiveTurn.ID, Owner: store.GenerationLeaseOwner(s.ownerUUID), Kind: "steer", Message: target.Content}); err != nil {
			writeErrorClass(w, http.StatusServiceUnavailable, "control_delivery_pending", "steering delivery is pending reconciliation")
			return
		}
	} else {
		details, err := s.runtimeGenerationDetails(r.Context(), session.ID, session.ActiveGenerationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Deliver before recording, exactly as Stop does. The command names the turn it
		// is for, so if that turn ends first the sandbox watcher discards the entry and
		// RecordSteeredTurn refuses below — the row stays queued and the user can still
		// send it as a turn of its own. Recording first would instead retire the row
		// against a delivery that never landed.
		if _, err := bridge.AppendTurnControl(details.BridgeDirPath, bridge.TurnControlCommand{
			TurnID:  snapshot.ActiveTurn.ID,
			Command: bridge.TurnControlSteer,
			Message: target.Content,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("deliver steer to sandbox: %v", err))
			return
		}
	}
	result, err := s.store.RecordSteeredTurn(r.Context(), store.RecordSteeredTurnParams{
		CommandID: commandID, GenerationID: session.ActiveGenerationID, Owner: store.GenerationLeaseOwner(s.ownerUUID),
		SessionID:  session.ID,
		TurnID:     target.ID,
		IntoTurnID: snapshot.ActiveTurn.ID,
		Now:        time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, store.ErrTurnNotSteerable) {
			writeErrorClass(w, http.StatusConflict, "turn_not_steerable", "the running turn ended before the message reached it")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The transcript entry is broadcast the same way a dispatched turn's prompt is,
	// so an open client shows what the user said mid-answer without a reload.
	s.hub.Publish(events.Event{Type: "message.created", SessionID: session.ID, Payload: result.Message})
	s.publishSessionRuntime(session.ID, result.Runtime)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "steered",
		"message": result.Message,
		"runtime": result.Runtime,
	})
}
