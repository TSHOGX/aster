package server

import (
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// sessionAcceptsWorkspaceWrite reports whether a session is in a state where the
// orchestrator may safely mutate its workspace from underneath the agent. It is
// false when the session status cannot accept input (busy/checkpointing/etc.)
// or when any turn is queued/leased/running. Handlers translate a false result
// into a 409 with error_class "turn_in_progress".
func (s *Server) sessionAcceptsWorkspaceWrite(ctx context.Context, session store.Session) (bool, error) {
	if !sessionstate.CanAcceptInput(session.Status) {
		return false, nil
	}
	active, err := s.store.SessionHasActiveTurn(ctx, session.ID)
	if err != nil {
		return false, err
	}
	return !active, nil
}

// uploadArtifacts handles POST /api/sessions/{id}/artifacts: a multipart upload
// that writes one or more files into the session workspace.
//
// Multipart scheme (the frontend MUST match this):
//   - Each uploaded file is its own multipart file part.
//   - The part's FORM FIELD NAME is the workspace-relative destination path
//     using forward slashes (e.g. field name "src/utils/a.ts"). This is what
//     the browser's webkitRelativePath yields for folder uploads; for a plain
//     multi-file selection the field name is just the filename.
//   - The part's own filename (FileHeader.Filename) is ignored for path
//     purposes; only the field name determines where the file lands.
//
// The response is {"uploaded":[<clean relative paths>...]}.
func (s *Server) uploadArtifacts(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	// Per-user upload permission (deny-by-default; see authRegistry.uploadAllowed).
	// This is the enforcement point: the frontend also withholds the upload entry
	// points for a denied user, but that is cosmetic and must not be relied on.
	if !s.creds.uploadAllowed(userIDFromContext(r.Context())) {
		writeErrorClass(w, http.StatusForbidden, "upload_not_permitted", "this account is not permitted to upload files")
		return
	}
	accepts, err := s.sessionAcceptsWorkspaceWrite(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !accepts {
		writeErrorClass(w, http.StatusConflict, "turn_in_progress", "session is busy; cannot upload while a turn is in progress")
		return
	}

	maxUpload := s.cfg.MaxUploadBytes
	maxFiles := s.cfg.MaxUploadFiles
	// Bound the whole request body. Add slack for multipart framing overhead
	// (boundaries, headers) on top of the decoded-content budget.
	const multipartSlack = 1 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+multipartSlack)

	// ParseMultipartForm buffers up to maxMemory in RAM and spills the rest to
	// temp files; keep the in-memory portion small since files stream to disk.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErrorClass(w, http.StatusRequestEntityTooLarge, "upload_too_large", "upload exceeds maximum allowed size")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File) == 0 {
		writeError(w, http.StatusBadRequest, "no files in upload")
		return
	}

	// Count total parts first so we reject an over-count before writing anything.
	total := 0
	for _, headers := range r.MultipartForm.File {
		total += len(headers)
	}
	if total > maxFiles {
		writeErrorClass(w, http.StatusRequestEntityTooLarge, "too_many_files",
			"upload exceeds the maximum number of files ("+strconv.Itoa(maxFiles)+")")
		return
	}

	uploaded := make([]string, 0, total)
	for relPath, headers := range r.MultipartForm.File {
		for _, header := range headers {
			cleanPath, status, message := s.writeUploadedPart(r, sessionID, relPath, header, maxUpload)
			if status != 0 {
				// Best-effort: surface the failure but leave already-written files
				// in place (the watcher will pick them up). The frontend can retry.
				writeErrorClass(w, status, errorClassForStatus(status), message)
				return
			}
			uploaded = append(uploaded, cleanPath)
		}
	}

	// Belt-and-suspenders: the fsnotify watcher normally records new files
	// automatically, but a synchronous scan guarantees the artifact list and SSE
	// broadcasts are up to date by the time the client re-fetches.
	if s.watcher != nil {
		if err := s.watcher.ScanSession(r.Context(), sessionID); err != nil {
			s.log.Warn("scan session after upload failed", "session_id", sessionID, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"uploaded": uploaded})
}

// writeUploadedPart streams a single multipart file part into the workspace at
// relPath. It returns (cleanPath, 0, "") on success or ("", status, message) on
// failure.
func (s *Server) writeUploadedPart(r *http.Request, sessionID, relPath string, header *multipart.FileHeader, maxBytes int64) (string, int, string) {
	file, err := header.Open()
	if err != nil {
		return "", http.StatusBadRequest, "cannot read uploaded file"
	}
	defer file.Close()

	cleanPath, writeErr := s.writeWorkspaceFile(r.Context(), sessionID, relPath, file, maxBytes)
	if writeErr != nil {
		status, message := writeWorkspaceStatus(writeErr)
		return "", status, message
	}
	return cleanPath, 0, ""
}

// writeArtifact handles PUT /artifacts/{sessionID}/{path}: overwrite a single
// workspace file from the online editor, with optimistic concurrency via
// If-Unmodified-Since.
func (s *Server) writeArtifact(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/artifacts/"), "/", 2)
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, "invalid artifact path")
		return
	}
	sessionID, artifactPath := parts[0], parts[1]

	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	accepts, err := s.sessionAcceptsWorkspaceWrite(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !accepts {
		writeErrorClass(w, http.StatusConflict, "turn_in_progress", "session is busy; cannot edit while a turn is in progress")
		return
	}

	// Optimistic concurrency: if the client sent If-Unmodified-Since, reject the
	// write when the file has been modified more recently (the agent changed it
	// since the editor loaded). Compare at second granularity to match the
	// Last-Modified header emitted by http.ServeContent.
	if ius := r.Header.Get("If-Unmodified-Since"); ius != "" {
		if since, parseErr := http.ParseTime(ius); parseErr == nil {
			stale, status, message := s.artifactIsStale(r, sessionID, artifactPath, since)
			if status != 0 {
				writeErrorClass(w, status, errorClassForStatus(status), message)
				return
			}
			if stale {
				writeErrorClass(w, http.StatusConflict, "stale_edit", "file was modified since it was loaded; reload before saving")
				return
			}
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxEditBytes+1)
	cleanPath, writeErr := s.writeWorkspaceFile(r.Context(), sessionID, artifactPath, r.Body, s.cfg.MaxEditBytes)
	if writeErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(writeErr, &maxErr) {
			writeErrorClass(w, http.StatusRequestEntityTooLarge, "edit_too_large", "file exceeds maximum allowed size")
			return
		}
		status, message := writeWorkspaceStatus(writeErr)
		writeErrorClass(w, status, errorClassForStatus(status), message)
		return
	}

	if s.watcher != nil {
		if err := s.watcher.ScanSession(r.Context(), sessionID); err != nil {
			s.log.Warn("scan session after edit failed", "session_id", sessionID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": cleanPath})
}

// artifactIsStale reports whether the target file's modification time is newer
// than since. It reuses openArtifactFile for the same path hardening, and passes
// accessOwner because it only ever runs on the owner-gated PUT path (writeArtifact
// calls authorizeSession before reaching here). A missing file is not stale (the
// edit creates it).
func (s *Server) artifactIsStale(r *http.Request, sessionID, artifactPath string, since time.Time) (bool, int, string) {
	file, info, status, message := s.openArtifactFile(r.Context(), sessionID, artifactPath, accessOwner)
	if file == nil {
		if status == http.StatusNotFound {
			return false, 0, ""
		}
		return false, status, message
	}
	defer file.Close()
	// Truncate to second granularity: HTTP dates carry no sub-second component.
	return info.ModTime().Truncate(time.Second).After(since), 0, ""
}

// errorClassForStatus maps an HTTP status to a stable error_class string for the
// JSON error body, so the frontend can branch on machine-readable classes.
func errorClassForStatus(status int) string {
	switch status {
	case http.StatusNotFound:
		return "not_found"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	case http.StatusBadRequest:
		return "invalid_path"
	default:
		return "internal_error"
	}
}
