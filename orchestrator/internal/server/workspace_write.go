package server

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
	"io"
	"net/http"
	"time"

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
	backend, err := s.store.SessionExecutionBackend(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if backend == "khora" {
		if s.workspaceWrites == nil {
			return "", newWorkspaceWriteError(http.StatusServiceUnavailable, "workspace backend unavailable")
		}
		file, err := s.workspaceWrites.WriteSession(ctx, sessionID, store.GenerationLeaseOwner(s.ownerUUID), relPath, content, maxBytes)
		return file.Path, err
	}
	if backend != "legacy" {
		return "", newWorkspaceWriteError(http.StatusServiceUnavailable, "unsupported workspace backend")
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
	owner := s.ownerUUID
	if owner == "" {
		owner = "local-" + uuid.NewString()
	}
	mutation := workspace.Mutation{ID: uuid.NewString(), SessionID: sessionID, Owner: owner, Path: relPath}
	if err := s.store.BeginWorkspaceMutation(ctx, mutation); err != nil {
		return "", err
	}
	file, err := workspace.Write(ctx, localworkspace.Directory{Path: volume.HostPath, UID: volume.SandboxUID, GID: volume.SandboxGID}, relPath, content, maxBytes)
	mutation.File = file
	mutation.State = "succeeded"
	if err != nil {
		mutation.State = "failed"
		mutation.Error = err.Error()
		if errors.Is(err, workspace.ErrUncertain) {
			mutation.State = "uncertain"
		}
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	finishErr := s.store.FinishWorkspaceMutation(finishCtx, mutation)
	return file.Path, errors.Join(err, finishErr)
}
