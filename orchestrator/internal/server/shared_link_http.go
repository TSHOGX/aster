package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// The anonymous public-link surface. This is a deliberate SECOND copy of the
// read handlers rather than a way to mint a cookie from a token and reuse the
// authed tree:
//
//   - Isolation. The authed routes never learn about an anonymous principal, so
//     a check missed over there cannot become anonymous access over here.
//   - GET-only, no cookie: no CSRF surface, and it works in a private window or
//     from a link-preview fetcher.
//
// The cost is a duplicated read path. The duplication is the point: these DTOs
// must be trimmed harder than the authed ones, and sharing code would make that
// trimming a conditional somebody can get wrong.

// sharedLinkResolution is a token lookup result cached in-process.
type sharedLinkResolution struct {
	link      store.ShareLink
	ok        bool
	expiresAt time.Time
}

// sharedLinkCacheTTL bounds how stale a token resolution may be. A revoke or a
// rotate therefore takes effect within this window rather than instantly. That
// is the deliberate trade for keeping anonymous traffic off the writer pool's
// single connection; anything needing immediate cutoff should also expire.
const sharedLinkCacheTTL = 10 * time.Second

// shareLinkAccessBuffer accumulates access counts in memory. Anonymous reads do
// not write to the database on the request path: the writer pool is one
// connection shared with every session and turn write, and a public link is the
// one anonymous surface that can be fetched in bulk. RunMaintenance flushes this
// periodically, so a restart loses at most one window of counts -- accurate
// enough for an audit trail, not worth a write lock.
type shareLinkAccessBuffer struct {
	mu      sync.Mutex
	pending map[string]*store.ShareLinkAccess
}

func (b *shareLinkAccessBuffer) record(linkID string, now time.Time) {
	if linkID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = make(map[string]*store.ShareLinkAccess)
	}
	entry, ok := b.pending[linkID]
	if !ok {
		b.pending[linkID] = &store.ShareLinkAccess{LinkID: linkID, Count: 1, LastSeen: now}
		return
	}
	entry.Count++
	if now.After(entry.LastSeen) {
		entry.LastSeen = now
	}
}

// drain removes and returns the buffered counts.
func (b *shareLinkAccessBuffer) drain() []store.ShareLinkAccess {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	accesses := make([]store.ShareLinkAccess, 0, len(b.pending))
	for _, entry := range b.pending {
		accesses = append(accesses, *entry)
	}
	b.pending = nil
	return accesses
}

// restore puts counts back after a failed flush, so a transient DB error does
// not silently discard them.
func (b *shareLinkAccessBuffer) restore(accesses []store.ShareLinkAccess) {
	if len(accesses) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = make(map[string]*store.ShareLinkAccess)
	}
	for _, access := range accesses {
		entry, ok := b.pending[access.LinkID]
		if !ok {
			copied := access
			b.pending[access.LinkID] = &copied
			continue
		}
		entry.Count += access.Count
		if access.LastSeen.After(entry.LastSeen) {
			entry.LastSeen = access.LastSeen
		}
	}
}

// flushShareLinkAccess persists buffered access counters. Called from the
// maintenance loop, never from a request.
func (s *Server) flushShareLinkAccess(ctx context.Context) error {
	accesses := s.shareAccess.drain()
	if len(accesses) == 0 {
		return nil
	}
	if err := s.store.TouchShareLinkAccess(ctx, accesses); err != nil {
		s.shareAccess.restore(accesses)
		return err
	}
	return nil
}

// lookupSharedLinkCached resolves a token through a short in-process cache, so a
// link being hammered does not issue a database read per request. Negative
// results are cached too: that is what keeps token-guessing traffic off the
// database entirely.
func (s *Server) lookupSharedLinkCached(r *http.Request, token string, now time.Time) (store.ShareLink, bool, error) {
	tokenHash := hashAuthToken(token)
	if resolution, ok := s.cachedSharedLink(tokenHash, now); ok {
		if resolution.ok && !now.Before(resolution.link.ExpiresAt) {
			// Cached while live, expired since. Fall through to a fresh lookup
			// rather than serving a link that has aged out inside the window.
			return store.ShareLink{}, false, nil
		}
		return resolution.link, resolution.ok, nil
	}
	link, ok, err := s.store.LookupShareLink(r.Context(), tokenHash, now)
	if err != nil {
		return store.ShareLink{}, false, err
	}
	s.storeSharedLink(tokenHash, sharedLinkResolution{
		link:      link,
		ok:        ok,
		expiresAt: now.Add(sharedLinkCacheTTL),
	})
	return link, ok, nil
}

func (s *Server) cachedSharedLink(tokenHash string, now time.Time) (sharedLinkResolution, bool) {
	s.shareLinkCacheMu.Lock()
	defer s.shareLinkCacheMu.Unlock()
	resolution, ok := s.shareLinkCache[tokenHash]
	if !ok || now.After(resolution.expiresAt) {
		return sharedLinkResolution{}, false
	}
	return resolution, true
}

func (s *Server) storeSharedLink(tokenHash string, resolution sharedLinkResolution) {
	s.shareLinkCacheMu.Lock()
	defer s.shareLinkCacheMu.Unlock()
	if s.shareLinkCache == nil {
		s.shareLinkCache = make(map[string]sharedLinkResolution)
	}
	// Bound the map so unbounded token guessing cannot grow it without limit.
	if len(s.shareLinkCache) >= maxSharedLinkCacheEntries {
		s.shareLinkCache = make(map[string]sharedLinkResolution, 1)
	}
	s.shareLinkCache[tokenHash] = resolution
}

// invalidateSharedLinkCache drops a session's cached token resolutions so an
// owner's revoke or rotate is not masked by the cache for its full TTL. Keyed by
// token hash, so this clears the whole map: a revoke is rare and the cache
// refills in one request each.
func (s *Server) invalidateSharedLinkCache() {
	s.shareLinkCacheMu.Lock()
	defer s.shareLinkCacheMu.Unlock()
	s.shareLinkCache = nil
}

// maxSharedLinkCacheEntries caps the token resolution cache.
const maxSharedLinkCacheEntries = 4096

// sharedMessages serves GET /api/shared/{token}/messages.
//
// max_message_id freezes the conversation at the point the link was created:
// sharing once must not mean publishing every future turn as well.
func (s *Server) sharedMessages(w http.ResponseWriter, r *http.Request, link store.ShareLink) {
	messages, err := s.store.ListMessages(r.Context(), link.SessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": sharedLinkMessages(messages, link),
	})
}

// sharedArtifacts serves GET /api/shared/{token}/artifacts: the link's frozen
// whitelist, NOT the session's live workspace. The distinction is the whole
// point -- ListArtifacts returns everything the watcher ever saw, including
// intermediate dumps the owner never meant to publish.
func (s *Server) sharedArtifacts(w http.ResponseWriter, r *http.Request, link store.ShareLink) {
	files, err := s.store.ListShareLinkFiles(r.Context(), link.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": sharedLinkFiles(files)})
}

// sharedAPI dispatches the unauthenticated /api/shared/{token}/... subtree.
// Every path here is GET-only; anything else is a 405 without a token lookup.
func (s *Server) sharedAPI(w http.ResponseWriter, r *http.Request) {
	s.applySharedHeaders(w, r)
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/shared/")
	parts := strings.Split(rest, "/")
	token := parts[0]
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	link, session, ok := s.resolveSharedLink(w, r, token)
	if !ok {
		return
	}
	scope := store.MessagePageParams{SessionID: link.SessionID, Summary: link.Visibility != store.ShareVisibilityFull, MaxID: link.MaxMessageID}
	switch {
	case len(parts) == 3 && parts[1] == "messages" && parts[2] == "page":
		s.serveMessagePage(w, r, scope)
	case len(parts) == 4 && parts[1] == "messages" && (parts[3] == "content" || parts[3] == "trace-content"):
		s.serveMessageContent(w, r, scope, parts[2])
	case len(parts) == 4 && parts[1] == "messages" && parts[3] == "trace":
		s.serveMessageTrace(w, r, scope, parts[2])
	case len(parts) == 4 && parts[1] == "messages" && parts[3] == "mentioned-files":
		s.serveMentionedFiles(w, r, scope, parts[2], link.ID)
	case len(parts) == 3 && parts[1] == "artifacts" && (parts[2] == "children" || parts[2] == "search"):
		s.serveArtifactPage(w, r, store.ArtifactPageParams{SessionID: link.SessionID, LinkID: link.ID})
	case len(parts) == 1:
		writeJSON(w, http.StatusOK, sharedLinkSession(session, link))
	case len(parts) == 2 && parts[1] == "messages":
		s.sharedMessages(w, r, link)
	case len(parts) == 2 && parts[1] == "artifacts":
		s.sharedArtifacts(w, r, link)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// applySharedHeaders sets the response headers specific to the anonymous
// subtree. Referrer-Policy is the important one: the token sits in the URL path,
// so any outbound link a shared page contains would otherwise leak it in the
// Referer header. The app-wide strict-origin-when-cross-origin still sends the
// origin, which is fine for pages but not for a bearer token.
func (s *Server) applySharedHeaders(w http.ResponseWriter, r *http.Request) {
	s.applyHSTS(w, r)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

// resolveSharedLink turns a URL token into a live link plus its session. Unknown,
// expired, revoked, and rotated-away tokens are all the same 404: a public URL
// must not reveal that it once worked.
func (s *Server) resolveSharedLink(w http.ResponseWriter, r *http.Request, token string) (store.ShareLink, store.Session, bool) {
	now := time.Now().UTC()
	link, ok, err := s.lookupSharedLinkCached(r, token, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return store.ShareLink{}, store.Session{}, false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return store.ShareLink{}, store.Session{}, false
	}
	session, err := s.store.GetSession(r.Context(), link.SessionID)
	if err != nil {
		// A link whose session row is gone is indistinguishable from no link.
		writeError(w, http.StatusNotFound, "not found")
		return store.ShareLink{}, store.Session{}, false
	}
	s.shareAccess.record(link.ID, now)
	return link, session, true
}
