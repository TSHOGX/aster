package server

import (
	"errors"
	"net/http"
	"time"

	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
)

// Owner-facing management of public links. All owner-gated: minting a URL that
// works without a login is the strongest thing an owner can do with a session,
// and a reader must never be able to do it.

// createShareLink handles POST /api/sessions/{id}/share-links.
//
// The raw token is returned HERE AND ONLY HERE. Only its SHA-256 is stored, so
// there is no later endpoint that can show it again; a lost link is replaced by
// rotating, which invalidates the old URL.
func (s *Server) createShareLink(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	var req struct {
		Visibility string   `json:"visibility"`
		TTLSeconds int64    `json:"ttl_seconds"`
		Files      []string `json:"files"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	now := time.Now().UTC()
	validated, err := validateCreateShareLink(createShareLinkInput{
		Visibility: req.Visibility,
		TTL:        time.Duration(req.TTLSeconds) * time.Second,
		Files:      req.Files,
	}, session, shareLinkLimits{
		MaxTTL:   s.cfg.MaxShareLinkTTL,
		MaxFiles: s.cfg.MaxShareLinkFiles,
	}, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Freeze the message window at creation: the link publishes this
	// conversation, not this conversation's future.
	maxMessageID, err := s.latestMessageID(r, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	token, err := newAuthToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create share link")
		return
	}
	link := store.ShareLink{
		ID:           newID("slink"),
		TokenHash:    hashAuthToken(token),
		SessionID:    sessionID,
		Visibility:   validated.Visibility,
		MaxMessageID: maxMessageID,
		CreatedBy:    userIDFromContext(r.Context()),
		CreatedAt:    now,
		ExpiresAt:    validated.ExpiresAt,
	}
	// Snapshot the whitelisted files BEFORE inserting the link, so a link row
	// never exists pointing at bytes that were not stored. The reverse order would
	// leave a link serving 404s for its own files.
	files, status, message := s.snapshotShareLinkFiles(r, link.ID, sessionID, validated.Files)
	if status != 0 {
		writeErrorClass(w, status, errorClassForStatus(status), message)
		return
	}
	if err := s.store.CreateShareLink(r.Context(), link, files); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"link":  publicShareLink(link, len(files), now),
		"token": token,
		"path":  "/s/" + token,
	})
}

// snapshotShareLinkFiles copies each whitelisted workspace file into the
// content-addressed blob store and returns the rows to persist.
//
// Snapshotting, rather than reading the live workspace at serve time, buys three
// things: the bytes behind a published URL cannot change under an agent's later
// write, the link keeps working after the session is destroyed and its workspace
// reclaimed, and the anonymous request path never touches a live workspace at all
// (so the VerifySessionWorkspaceVolume evidence chain is not in the loop).
//
// The copy runs inside this request, which is why the caps are enforced here: a
// whole-session "select all" would otherwise be an unbounded synchronous copy.
func (s *Server) snapshotShareLinkFiles(r *http.Request, linkID, sessionID string, paths []string) ([]store.ShareLinkFile, int, string) {
	if len(paths) == 0 {
		return nil, 0, ""
	}
	if s.shareBlobs == nil {
		return nil, http.StatusServiceUnavailable, "file sharing is not configured on this deployment"
	}
	remaining := s.cfg.MaxShareLinkBytes
	if remaining <= 0 {
		return nil, http.StatusServiceUnavailable, "share link byte cap is not configured"
	}
	files := make([]store.ShareLinkFile, 0, len(paths))
	for _, path := range paths {
		// accessOwner: only the owner reaches this handler, and the snapshot reads
		// the live workspace exactly as an owner download would.
		file, _, status, message := s.openArtifactFile(r.Context(), sessionID, path, accessOwner)
		if file == nil {
			return nil, status, message
		}
		sum, size, err := s.shareBlobs.Put(file, remaining)
		_ = file.Close()
		if errors.Is(err, shareblob.ErrTooLarge) {
			return nil, http.StatusRequestEntityTooLarge, "selected files exceed the share link size limit"
		}
		if err != nil {
			return nil, http.StatusInternalServerError, err.Error()
		}
		remaining -= size
		cleanPath, status, message := validateArtifactRequestPath(sessionID, path)
		if status != 0 {
			return nil, status, message
		}
		files = append(files, store.ShareLinkFile{
			LinkID:     linkID,
			Path:       cleanPath,
			BlobSHA256: sum,
			Size:       size,
		})
	}
	return files, 0, ""
}

// listShareLinks handles GET /api/sessions/{id}/share-links.
func (s *Server) listShareLinks(w http.ResponseWriter, r *http.Request, sessionID string) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	links, err := s.store.ListShareLinks(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	items := make([]apiShareLink, 0, len(links))
	for _, link := range links {
		files, err := s.store.ListShareLinkFiles(r.Context(), link.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, publicShareLink(link, len(files), now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": items})
}

// rotateShareLink handles POST /api/sessions/{id}/share-links/{linkID}/rotate:
// issue a new token for an existing link, invalidating the URL already handed
// out while keeping the link's window, visibility and expiry.
func (s *Server) rotateShareLink(w http.ResponseWriter, r *http.Request, sessionID, linkID string) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	token, err := newAuthToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate share link")
		return
	}
	err = s.store.RotateShareLinkToken(r.Context(),
		store.RevokeShareParams{SessionID: sessionID, ID: linkID}, hashAuthToken(token))
	if errors.Is(err, store.ErrShareNotFound) {
		writeError(w, http.StatusNotFound, "share link not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Drop cached resolutions so the retired token stops working now rather than
	// at the end of the cache window.
	s.invalidateSharedLinkCache()
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "path": "/s/" + token})
}

// deleteShareLink handles DELETE /api/sessions/{id}/share-links/{linkID}.
func (s *Server) deleteShareLink(w http.ResponseWriter, r *http.Request, sessionID, linkID string) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	err := s.store.RevokeShareLink(r.Context(), store.RevokeShareParams{
		SessionID: sessionID,
		ID:        linkID,
		Now:       time.Now().UTC(),
	})
	if errors.Is(err, store.ErrShareNotFound) {
		writeError(w, http.StatusNotFound, "share link not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.invalidateSharedLinkCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// latestMessageID returns the id of the session's newest message, or nil when it
// has none (an empty session's link simply has no window to freeze).
func (s *Server) latestMessageID(r *http.Request, sessionID string) (*int64, error) {
	messages, err := s.store.ListMessages(r.Context(), sessionID)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}
	// ListMessages orders by id ascending.
	maxID := messages[len(messages)-1].ID
	return &maxID, nil
}
