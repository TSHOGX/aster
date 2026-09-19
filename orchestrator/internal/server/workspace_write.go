package server

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"

	"harness-platform/orchestrator/internal/workspace"
	localworkspace "harness-platform/orchestrator/internal/workspace/local"
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
	case errors.Is(err, workspace.ErrInvalidPath):
		return http.StatusBadRequest, "invalid artifact path"
	case errors.Is(err, workspace.ErrForbidden):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, workspace.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, workspace.ErrUncertain):
		return http.StatusServiceUnavailable, err.Error()
	}
	return http.StatusInternalServerError, err.Error()
}

// writeWorkspaceFile resolves the authorized volume, then delegates portable
// mutation policy and descriptor-relative file I/O to the workspace service.
func (s *Server) writeWorkspaceFile(ctx context.Context, sessionID, relPath string, content io.Reader, maxBytes int64) (string, error) {
	if _, err := workspace.CleanPath(relPath); err != nil {
		return "", err
	}
	volumeConfig, err := s.dataVolumeProvisionerConfig()
	if err != nil {
		return "", err
	}
	volume, err := (localworkspace.Volumes{Records: s.store}).VerifySessionWorkspaceVolume(ctx, localworkspace.VerifySessionWorkspaceVolumeParams{SessionID: sessionID, Config: volumeConfig})
	if errors.Is(err, sql.ErrNoRows) {
		return "", newWorkspaceWriteError(http.StatusNotFound, "session not found")
	}
	if err != nil {
		return "", newWorkspaceWriteError(http.StatusForbidden, "workspace evidence invalid")
	}
	file, err := workspace.Write(ctx, localworkspace.Directory{Path: volume.HostPath, UID: volume.SandboxUID, GID: volume.SandboxGID}, relPath, content, maxBytes)
	return file.Path, err
}
