package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/store"
)

// sandboxExitedErrorClass marks a generation whose `runsc run` process returned
// while the generation was live (active/idle) and nobody on the host had asked
// it to stop. It is the honest name for what recovery used to file as
// orchestrator_restart_reconnect_grace_expired 90s later.
const sandboxExitedErrorClass = "sandbox_exited"

// sandboxExitRetireTimeout bounds the store work done from the container's
// monitor goroutine. The next container for the same session cannot register
// until this returns, so it must stay short.
const sandboxExitRetireTimeout = 15 * time.Second

// HandleSandboxExit is the runtime's ContainerExitHandler. It runs the moment
// `runsc run` returns and, when the exit was not requested by the host, retires
// the generation at once: the generation is failed with the sandbox's last
// stderr line as the reason, an in-flight running turn is failed and the user
// gets the transcript notice, and the session drops to running_idle so the next
// message cold-starts. Until this existed the same outcome took lease TTL +
// reconnect grace to arrive, under a misleading error class, with the
// sandbox's own output discarded.
//
// Deaths during start (generation allocating/starting/probing) are left to the
// start path, which already owns failure there; deaths after the generation
// has been retired (release, interrupt, destroy) are no-ops because the status
// check below sees a terminal row. Every CAS in FailGenerationWithEvent also
// protects against the remaining races: if the generation was retired between
// the status read and the write, the write touches nothing and lease recovery
// stays the backstop.
func (s *Server) HandleSandboxExit(exit runtime.ContainerExit) {
	if exit.Intentional {
		s.log.Debug("sandbox stopped by host", "session_id", exit.SessionID, "generation_id", exit.GenerationID, "exit", exit.ExitStatus())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandboxExitRetireTimeout)
	defer cancel()
	status, err := s.store.GetRuntimeGenerationStatus(ctx, exit.SessionID, exit.GenerationID)
	if err != nil {
		s.log.Warn("sandbox exited but generation status lookup failed", "session_id", exit.SessionID, "generation_id", exit.GenerationID, "error", err)
		return
	}
	switch status {
	case "active", "idle":
	default:
		s.log.Info("sandbox exited outside live states; owning path handles it",
			"session_id", exit.SessionID, "generation_id", exit.GenerationID, "generation_status", status, "exit", exit.ExitStatus(), "log", exit.LogPath)
		return
	}
	// This is the record that never existed before: the sandbox's own last words.
	s.log.Error("sandbox exited unexpectedly",
		"session_id", exit.SessionID, "generation_id", exit.GenerationID,
		"generation_status", status, "exit", exit.ExitStatus(), "log", exit.LogPath,
		"tail", strings.Join(exit.Tail, "\n"))

	cause := sandboxExitedErrorClass
	if killed, ok := s.runtime.SandboxOOMKilled(ctx, exit.RunscContainerID); ok && killed {
		cause = sandboxOOMCause
	}
	turnID, turnStatus, inflight, err := s.store.InflightTurnForGeneration(ctx, exit.SessionID, exit.GenerationID)
	if err != nil {
		s.log.Warn("sandbox exit: in-flight turn lookup failed", "session_id", exit.SessionID, "generation_id", exit.GenerationID, "error", err)
		return
	}
	params := store.FailGenerationParams{
		SessionID:    exit.SessionID,
		GenerationID: exit.GenerationID,
		Owner:        store.GenerationLeaseOwner(s.ownerUUID),
		ErrorClass:   cause,
		Reason:       sandboxExitReason(cause, exit),
		Now:          time.Now().UTC(),
	}
	// A running turn has crossed the ack fence: the agent had it and its context
	// died with the sandbox, so it fails and the user is told. A leased turn was
	// never acknowledged; leaving it alone lets ReconcileStaleSessionStates
	// requeue it against the replacement generation rather than losing the
	// prompt here.
	runningTurn := inflight && turnStatus == "running"
	if runningTurn {
		params.TurnID = turnID
	}
	result, err := s.store.FailGenerationWithEvent(ctx, params)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("sandbox exit: generation retire CAS failed, leaving to lease recovery",
				"session_id", exit.SessionID, "generation_id", exit.GenerationID, "error", err)
		}
		return
	}
	if runningTurn {
		notice := sandboxDeathNotice(cause, s.cfg.Harness.Runtime.SandboxMemoryLimitBytes)
		if _, err := s.store.AddAssistantMessage(ctx, exit.SessionID, "assistant", notice, sandboxDeathStopReason); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("failed to record sandbox death notice", "session_id", exit.SessionID, "generation_id", exit.GenerationID, "error", err)
		}
	}
	s.publishDurableEvent(ctx, result.EventID)
	s.publishSessionRuntimeFromStore(ctx, exit.SessionID)
}

// sandboxExitReason is the failure_reason persisted on the generation and
// carried in the generation.error payload. It names the exit status and quotes
// the sandbox's last stderr line, which for a bridge client crash is the tail of
// the Python traceback — the one line that answers "why".
func sandboxExitReason(cause string, exit runtime.ContainerExit) string {
	var b strings.Builder
	if cause == sandboxOOMCause {
		b.WriteString("sandbox killed by memory limit")
	} else {
		b.WriteString("sandbox process exited (")
		b.WriteString(exit.ExitStatus())
		b.WriteString(")")
	}
	if last := exit.LastStderr(); last != "" {
		b.WriteString(": ")
		if len(last) > 200 {
			last = last[:200] + "…"
		}
		b.WriteString(last)
	}
	return b.String()
}
