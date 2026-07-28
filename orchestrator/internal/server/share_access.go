package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"harness-platform/orchestrator/internal/store"
)

// accessLevel is what a caller may do with a session. It is ordered: a higher
// level implies every lower one.
type accessLevel int

const (
	// accessNone: the caller may not know the session exists.
	accessNone accessLevel = iota
	// accessReader: the caller may read, subject to a visibility level.
	accessReader
	// accessOwner: the caller owns the session and may read and write it.
	accessOwner
)

// resolveAccess loads a session and reports what viewerID may do with it, plus
// the visibility that applies (owner is always "full").
//
// Owner is checked first and short-circuits: an owner's read must not pay for a
// share lookup, because the writer pool is one connection shared with every
// session and turn write.
//
// This is deliberately NOT a relaxation of authorizeSession. That function keeps
// its owner-only meaning and still guards every write route; the read gate is
// swapped in only on the four routes that are read-only. Any route nobody
// remembered to convert therefore stays owner-only — fail closed. Widening the
// shared gate and then re-tightening the write routes one by one would fail the
// other way, where a single miss is an unauthorized write.
func (s *Server) resolveAccess(ctx context.Context, sessionID, viewerID string) (store.Session, accessLevel, string, error) {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return store.Session{}, accessNone, "", err
	}
	if session.UserID == viewerID {
		return session, accessOwner, store.ShareVisibilityFull, nil
	}
	share, ok, err := s.store.ResolveSessionShare(ctx, sessionID, viewerID)
	if err != nil {
		return store.Session{}, accessNone, "", err
	}
	if !ok {
		return store.Session{}, accessNone, "", nil
	}
	visibility := share.Visibility
	if visibility == "" {
		visibility = store.ShareVisibilitySummary
	}
	return session, accessReader, visibility, nil
}

// authorizeRead is the read gate for the four read-only session routes
// (getSession, listMessages, listArtifacts, artifact GET). It writes the same
// indistinguishable 404 as authorizeSession for a missing session, an owner
// mismatch with no grant, or a revoked grant.
func (s *Server) authorizeRead(w http.ResponseWriter, r *http.Request, sessionID string) (store.Session, accessLevel, string, bool) {
	session, level, visibility, err := s.resolveAccess(r.Context(), sessionID, userIDFromContext(r.Context()))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session not found")
		return store.Session{}, accessNone, "", false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return store.Session{}, accessNone, "", false
	}
	if level == accessNone {
		writeError(w, http.StatusNotFound, "session not found")
		return store.Session{}, accessNone, "", false
	}
	return session, level, visibility, true
}
