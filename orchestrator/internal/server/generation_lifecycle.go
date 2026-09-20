package server

import (
	"context"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
	"net/http"
)

func (s *Server) ensureActiveGeneration(ctx context.Context, session store.Session, owner string) (execution.Binding, error) {
	if s.executions == nil {
		return execution.Binding{}, fmt.Errorf("Khora execution controller unavailable")
	}
	return s.executions.Ensure(ctx, execution.Session{ID: session.ID, Driver: session.Agent, ActiveGenerationID: session.ActiveGenerationID}, owner)
}
func (s *Server) releaseScheduledIdle(ctx context.Context, session store.Session, owner string) error {
	if s.executions == nil {
		return fmt.Errorf("Khora controller unavailable")
	}
	if err := s.executions.ReleaseIdle(ctx, session.ID, session.ActiveGenerationID, owner); err != nil {
		return err
	}
	s.publishSessionRuntimeFromStore(ctx, session.ID)
	return nil
}
func (s *Server) ensureRunnableGenerationHTTP(w http.ResponseWriter, r *http.Request, session store.Session) bool {
	if !sessionstate.CanAcceptInput(session.Status) && session.Status != string(sessionstate.RunningActive) {
		writeError(w, http.StatusConflict, "session is "+session.Status)
		return false
	}
	if session.Status == string(sessionstate.RunningActive) {
		return true
	}
	_, err := s.ensureActiveGeneration(r.Context(), session, store.GenerationLeaseOwner(s.ownerUUID))
	return s.writeEnsureGenerationError(w, err)
}
func (s *Server) writeEnsureGenerationError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrPoolExhausted), sandbox.Is(err, sandbox.Capacity):
		writeErrorClass(w, http.StatusServiceUnavailable, "pool_exhausted", "resource pool exhausted")
	case errors.Is(err, execution.ErrBusy), store.IsWorkspaceMutationBusy(err):
		writeError(w, http.StatusConflict, "session is busy")
	case sandbox.Is(err, sandbox.Unavailable), sandbox.Is(err, sandbox.Uncertain):
		writeErrorClass(w, http.StatusServiceUnavailable, "runtime_unavailable", "execution backend is reconciling")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}
