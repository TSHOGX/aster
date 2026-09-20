package server

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
	"io"
	"net/http"

	"harness-platform/orchestrator/internal/workspace"
)

// workspaceWriteError carries an HTTP status alongside a message so the caller
// can translate a failed workspace write into the right client response. It
// mirrors the (status, message) return shape of openArtifactFile.
type workspaceWriteError struct {
	status  int
	message string
}

func (e *workspaceWriteError) Error() string { return e.message }

func newWorkspaceWriteError(status int, message string) *workspaceWriteError {
	return &workspaceWriteError{status: status, message: message}
}

// writeWorkspaceStatus extracts the HTTP status and message from an error
// returned by writeWorkspaceFile, defaulting to 500 for unexpected errors.
func writeWorkspaceStatus(err error) (int, string) {
	var we *workspaceWriteError
	if errors.As(err, &we) {
		return we.status, we.message
	}
	switch {
	case errors.Is(err, workspace.ErrMutationBusy), store.IsWorkspaceMutationBusy(err):
		return http.StatusConflict, "session is busy; workspace mutation in progress"
	case errors.Is(err, workspace.ErrInvalidPath):
		return http.StatusBadRequest, "invalid artifact path"
	case errors.Is(err, workspace.ErrForbidden):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, workspace.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, workspace.ErrUncertain):
		return http.StatusServiceUnavailable, err.Error()
	case sandbox.Is(err, sandbox.Unavailable), sandbox.Is(err, sandbox.Uncertain), sandbox.Is(err, sandbox.Capacity):
		return http.StatusServiceUnavailable, "workspace backend unavailable"
	}
	return http.StatusInternalServerError, err.Error()
}

// writeWorkspaceFile resolves the authorized volume, then delegates portable
// mutation policy and descriptor-relative file I/O to the workspace service.
func (s *Server) writeWorkspaceFile(ctx context.Context, sessionID, relPath string, content io.Reader, maxBytes int64) (string, error) {
	if _, err := workspace.CleanPath(relPath); err != nil {
		return "", err
	}
	if s.workspaceWrites == nil {
		return "", newWorkspaceWriteError(http.StatusServiceUnavailable, "workspace backend unavailable")
	}
	file, err := s.workspaceWrites.WriteSession(ctx, sessionID, store.GenerationLeaseOwner(s.ownerUUID), relPath, content, maxBytes)
	return file.Path, err
}
