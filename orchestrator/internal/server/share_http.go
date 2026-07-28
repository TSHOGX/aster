package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// The owner-facing share management API. Every handler here goes through
// authorizeSession (owner-only): managing who can read a session is an owner
// power, and a reader must not be able to re-share what was shared with them.

// listUsers serves GET /api/users: the usernames the share picker offers.
//
// This hands the full user list to any logged-in caller. That is acceptable for
// an internal tool -- you cannot share with someone whose name you cannot type --
// and is recorded here as a deliberate choice rather than an oversight. In open
// mode (no credentials configured) the list is empty: there are no named users
// to share with.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	names := s.creds.usernames()
	users := make([]map[string]string, 0, len(names))
	viewer := userIDFromContext(r.Context())
	for _, name := range names {
		if name == viewer {
			continue
		}
		users = append(users, map[string]string{"user_id": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// createSessionShare handles POST /api/sessions/{id}/shares.
func (s *Server) createSessionShare(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	var req struct {
		Grantee    string `json:"grantee"`
		Visibility string `json:"visibility"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	owner := userIDFromContext(r.Context())
	validated, err := validateCreateShare(req.Grantee, req.Visibility, session, owner, s.knownUsernames())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	share := store.SessionShare{
		ID:            newID("share"),
		SessionID:     sessionID,
		GranteeUserID: validated.Grantee,
		Role:          store.ShareRoleReader,
		Visibility:    validated.Visibility,
		CreatedBy:     owner,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.store.CreateSessionShare(r.Context(), share); err != nil {
		// The live-grant unique index is the only expected conflict here.
		if isUniqueConstraintError(err) {
			writeErrorClass(w, http.StatusConflict, "share_exists", "session is already shared with that user")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyShareRecipient(r.Context(), share, session)
	writeJSON(w, http.StatusCreated, publicSessionShare(share))
}

// notifyShareRecipient tells the grantee they have been given access.
//
// Best-effort: the grant is already committed and is the thing that matters, so a
// notification failure is logged rather than failing the request or, worse,
// rolling back a share the owner was told nothing about.
func (s *Server) notifyShareRecipient(ctx context.Context, share store.SessionShare, session store.Session) {
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = session.ID
	}
	if _, err := s.createNotification(ctx, share.GranteeUserID,
		"A session was shared with you",
		share.CreatedBy+" shared \""+title+"\" with you (read-only)."); err != nil {
		s.log.Warn("failed to notify share recipient",
			"session_id", share.SessionID, "grantee", share.GranteeUserID, "error", err)
	}
}

// listSessionShares handles GET /api/sessions/{id}/shares.
func (s *Server) listSessionShares(w http.ResponseWriter, r *http.Request, sessionID string) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	shares, err := s.store.ListSessionShares(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": publicSessionShares(shares)})
}

// deleteSessionShare handles DELETE /api/sessions/{id}/shares/{shareID}.
func (s *Server) deleteSessionShare(w http.ResponseWriter, r *http.Request, sessionID, shareID string) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	err := s.store.RevokeSessionShare(r.Context(), store.RevokeShareParams{
		SessionID: sessionID,
		ID:        shareID,
		Now:       time.Now().UTC(),
	})
	if errors.Is(err, store.ErrShareNotFound) {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// knownUsernames returns the configured usernames as a set, or nil in open mode
// (where there is no user list to validate a grantee against).
func (s *Server) knownUsernames() map[string]struct{} {
	names := s.creds.usernames()
	if names == nil {
		return nil
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

// isUniqueConstraintError reports whether err is a SQLite uniqueness violation.
// Matched on message text because the driver does not export a typed code; the
// only unique index the share writes can hit is session_shares_live_idx.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
