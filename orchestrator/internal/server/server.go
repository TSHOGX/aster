package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/titler"
	"harness-platform/orchestrator/internal/workspace"
)

const labUserID = "lab"

var ()

type Server struct {
	retainedDrivers      map[agents.ID]bool
	executionReconciler  interface{ Run(context.Context) error }
	workspaceInventory   sessionInventoryScanner
	workspaceWrites      sessionWorkspaceWriter
	workspaceReads       sessionWorkspaceReader
	executions           sessionExecutionController
	nativeStateValidator func(context.Context, store.DriverStateValidationInput, int64) (workspace.File, error)
	cfg                  config.Config
	store                serverStore
	hub                  *events.Hub
	log                  *slog.Logger
	titler               *titler.Client
	upgrader             websocket.Upgrader
	ownerUUID            string
	version              string
	buildCommit          string

	creds        *authRegistry
	loginLimiter *auth.RateLimiter
	ipLimiter    *auth.IPRateLimiter

	// shareBlobs holds the frozen bytes public links serve. Nil disables file
	// sharing: links still work for conversation text, and the whitelist endpoints
	// report an empty set rather than falling back to the live workspace.
	shareBlobs *shareblob.Store

	sessionOwnerMu    sync.Mutex
	sessionOwnerCache map[string]sessionOwnerEntry

	bridgeProjectionMu sync.Mutex
	bridgeParserMu     sync.Mutex
	bridgeParsers      map[bridgeStreamParserKey]*streamParser

	// publicPayloads memoizes the sanitized client-facing payload per durable
	// event id so a single event fanned out to N subscribers is sanitized once
	// rather than N times on the SSE/WebSocket delivery path.
	publicPayloads publicPayloadCache

	// shareLinkCache memoizes public-link token resolutions, and shareAccess
	// buffers their access counters. Both exist to keep anonymous traffic off the
	// writer pool's single connection: a public link is the only unauthenticated
	// surface here and the only one that can be pulled in bulk.
	shareLinkCacheMu sync.Mutex
	shareLinkCache   map[string]sharedLinkResolution
	shareAccess      shareLinkAccessBuffer

	schedulerWake       chan struct{}
	schedulerRunsMu     sync.Mutex
	schedulerRunsActive map[string]struct{}

	// shutdownCh is closed before the HTTP servers begin graceful shutdown. Long
	// lived streams (SSE/WebSocket) select on it because net/http.Server.Shutdown
	// does not cancel active request contexts; without this signal they can keep
	// Shutdown waiting until its deadline expires.
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
}

func (s *Server) SetExecutionReconciler(reconciler interface{ Run(context.Context) error }) {
	s.executionReconciler = reconciler
}

type sessionInventoryScanner interface {
	ScanSession(context.Context, string) error
}

func (s *Server) SetWorkspaceInventory(scanner sessionInventoryScanner) {
	s.workspaceInventory = scanner
}

type sessionWorkspaceReader interface {
	OpenSession(context.Context, string, string, string) (io.ReadSeekCloser, workspace.File, error)
}

type sessionWorkspaceWriter interface {
	WriteSession(context.Context, string, string, string, io.Reader, int64) (workspace.File, error)
}

func (s *Server) SetWorkspaceWriter(writer sessionWorkspaceWriter) { s.workspaceWrites = writer }

func (s *Server) SetWorkspaceReader(reader sessionWorkspaceReader) { s.workspaceReads = reader }

type sessionExecutionController interface {
	Ensure(context.Context, execution.Session, string) (execution.Binding, error)
	ReleaseIdle(context.Context, string, string, string) error
}

// SetExecutionController configures the Khora execution port during startup.
// Creation policy affects new rows only; dispatch reads each persisted backend.

type sessionOwnerEntry struct {
	userID    string
	expiresAt time.Time
}

type userContextKey struct{}

type bridgeStore interface{ bridge.Store }

type bridgeStreamParserKey struct {
	SessionID    string
	GenerationID string
	TurnID       int64
}

func New(
	cfg config.Config,
	store serverStore,
	hub *events.Hub,
	log *slog.Logger,
) *Server {
	return &Server{
		cfg:   cfg,
		store: store,
		hub:   hub,
		log:   log,
		titler: titler.New(titler.Config{
			BaseURL: cfg.TitleLLM.BaseURL,
			APIKey:  cfg.TitleLLM.APIKey,
			Model:   cfg.TitleLLM.Model,
		}),
		creds:        newAuthRegistry(cfg, log),
		loginLimiter: auth.NewRateLimiter(5, 15*time.Minute),
		// ~10 req/s sustained per IP with a burst of 40 absorbs normal
		// interactive bursts and SSE reconnects while throttling scanners and
		// request floods. Tune via constants if legitimate traffic trips it.
		ipLimiter:           auth.NewIPRateLimiter(10, 40),
		sessionOwnerCache:   make(map[string]sessionOwnerEntry),
		schedulerWake:       make(chan struct{}, 1),
		schedulerRunsActive: make(map[string]struct{}),
		shutdownCh:          make(chan struct{}),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// BeginShutdown asks long-lived application streams to return. It is safe to
// call more than once and is deliberately separate from http.Server.Shutdown:
// the latter only stops accepting new connections and waits for active handlers
// to become idle, while stream handlers need an application-level stop signal.
func (s *Server) BeginShutdown() {
	if s == nil {
		return
	}
	s.shutdownOnce.Do(func() {
		if s.shutdownCh != nil {
			close(s.shutdownCh)
		}
	})
}

func (s *Server) shutdownSignal() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.shutdownCh
}

// SetShareBlobStore installs the snapshot store for public link files. It is
// injected rather than opened in New so the leaf package stays independent of
// config and so a deployment can leave it unset (text-only links).
func (s *Server) SetShareBlobStore(store *shareblob.Store) {
	s.shareBlobs = store
}

func (s *Server) SetOwnerUUID(ownerUUID string) {
	s.ownerUUID = strings.TrimSpace(ownerUUID)
}

func (s *Server) SetBuildCommit(commit string) { s.buildCommit = strings.TrimSpace(commit) }

func (s *Server) SetVersion(version string) {
	s.version = strings.TrimSpace(version)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/me", s.me)
	mux.Handle("POST /api/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	// The public-link subtree is registered OUTSIDE requireAuth. ServeMux picks
	// the most specific pattern, so "/api/shared/" wins over "/api/" for these
	// paths -- which is load-bearing, not incidental: if it ever fell through to
	// the authed branch every anonymous request would 401, and if the authed tree
	// ever swallowed it the handlers would run with a synthesized identity.
	// TestSharedSubtreeBypassesAuthWhileAPIRequiresIt pins both directions.
	mux.Handle("/api/shared/", http.HandlerFunc(s.sharedAPI))
	// Snapshot file bytes live on their own top-level path rather than under
	// /api/, so the frontend proxy for them is a separate route handler and an
	// HTML/PDF preview iframe has a URL that is clearly not an API call.
	mux.Handle("/shared/", http.HandlerFunc(s.sharedFile))
	mux.Handle("/api/", s.requireAuth(http.HandlerFunc(s.api)))
	// Administrator APIs are a separate subtree so authorization is impossible
	// to forget when adding an ordinary user-facing route. The inner handler
	// still validates the method/path and records mutations in the audit log.
	mux.Handle("/api/admin/", s.requireAdmin(http.HandlerFunc(s.adminAPI)))
	mux.Handle("/artifacts/", s.requireAuth(http.HandlerFunc(s.artifactFile)))
	return s.rateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
	}))
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := userIDFromContext(r.Context())
		if userID == labUserID || !s.creds.enabled() || s.creds.role(userID) != store.RoleAdmin {
			writeError(w, http.StatusForbidden, "administrator role required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) adminAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin")
	switch {
	case path == "/users" && r.Method == http.MethodGet:
		names := s.creds.usernames()
		users := make([]map[string]string, 0, len(names))
		for _, name := range names {
			users = append(users, map[string]string{"user_id": name, "role": s.creds.role(name)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	case path == "/audit" && r.Method == http.MethodGet:
		limit, offset := 50, 0
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
			limit = v
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
			offset = v
		}
		entries, err := s.store.ListAudit(r.Context(), limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read audit log")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "limit": limit, "offset": offset})
	case path == "/feature-flags" && r.Method == http.MethodGet:
		flags, err := s.store.ListFeatureFlags(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read feature flags")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
	case strings.HasPrefix(path, "/feature-flags/") && r.Method == http.MethodPatch:
		s.updateFeatureFlag(w, r, strings.TrimPrefix(path, "/feature-flags/"))
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) updateFeatureFlag(w http.ResponseWriter, r *http.Request, key string) {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "/\\") {
		writeError(w, http.StatusBadRequest, "invalid feature flag key")
		return
	}
	var req struct {
		Enabled     *bool  `json:"enabled"`
		Description string `json:"description"`
		Reason      string `json:"reason"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) || req.Enabled == nil {
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled is required")
		}
		return
	}
	actor := userIDFromContext(r.Context())
	before, found, err := s.store.GetFeatureFlag(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read feature flag")
		return
	}
	after, err := s.store.SetFeatureFlag(r.Context(), key, *req.Enabled, strings.TrimSpace(req.Description), actor, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update feature flag")
		return
	}
	entry := store.AuditEntry{ActorUserID: actor, Action: "feature_flag.update", TargetType: "feature_flag", TargetID: key, After: mustJSON(after), Reason: strings.TrimSpace(req.Reason), RequestID: r.Header.Get("X-Request-ID")}
	if found {
		entry.Before = mustJSON(before)
	}
	if err := s.store.AppendAudit(r.Context(), entry); err != nil {
		if s.log != nil {
			s.log.Error("feature flag updated but audit write failed", "key", key, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, after)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// rateLimit throttles requests per client IP with a token bucket, returning 429
// once a client outpaces the sustained rate. /healthz is exempt so liveness
// probes are never throttled. This is a coarse flood/scan guard layered on top
// of the per-(username,IP) login lockout.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.ipLimiter.Allowed(s.clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) OperatorRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents", s.operatorAgentsCatalog)
	return mux
}

func (s *Server) operatorAgentsCatalog(w http.ResponseWriter, r *http.Request) {
	drivers := []driverDTO{}
	for _, spec := range agents.AllDriverSpecs() {
		drivers = append(drivers, driverDTO{
			DriverID:                    string(spec.ID),
			Label:                       spec.Label,
			Kind:                        string(spec.Kind),
			BridgeProtocol:              spec.BridgeProtocol,
			OutputSchema:                spec.OutputSchema,
			RequiredRuntimeCapabilities: append([]string(nil), spec.RequiredRuntimeCapabilities...),
			ModelAccess:                 spec.ModelAccess,
			SupportsInterrupt:           spec.SupportsInterrupt,
			SupportsCompaction:          spec.SupportsCompaction,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 1,
		"drivers":        drivers,
	})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version, "commit": s.buildCommit})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.creds.enabled() {
		// Open mode: issue a session for the default lab user so the cookie
		// flow is uniform, but no credentials are required.
		s.issueAuthSession(w, r, labUserID)
		writeJSON(w, http.StatusOK, map[string]string{"user_id": labUserID})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	rateKey := username + "|" + s.clientIP(r)
	if !s.loginLimiter.Allowed(rateKey) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}

	if userID, ok := s.verifyCredentials(username, req.Password); ok {
		s.loginLimiter.RecordSuccess(rateKey)
		s.issueAuthSession(w, r, userID)
		writeJSON(w, http.StatusOK, map[string]string{"user_id": userID})
		return
	}

	if locked := s.loginLimiter.RecordFailure(rateKey); locked {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	writeError(w, http.StatusUnauthorized, "invalid username or password")
}

// verifyCredentials validates a username/password against configured users or
// the single shared-password fallback. It always performs hashing work even
// when the user is unknown, to avoid leaking account existence via timing.
func (s *Server) verifyCredentials(username, password string) (string, bool) {
	// Force a reload check so a hash written seconds ago is honored, even when a
	// shared secret short-circuits enabled().
	s.creds.refresh(true)
	if s.creds.hasUsers() {
		user, ok := s.creds.lookup(username)
		if !ok {
			// Run a dummy verify to equalize timing for unknown users.
			auth.VerifyPassword("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
			return "", false
		}
		if auth.VerifyPassword(user.PasswordHash, password) {
			return user.Username, true
		}
		return "", false
	}
	// Shared-password fallback (legacy single-user lab mode).
	if s.cfg.SharedSecret != "" && hmac.Equal([]byte(password), []byte(s.cfg.SharedSecret)) {
		return labUserID, true
	}
	return "", false
}

// issueAuthSession creates a server-side login session and sets the cookie.
func (s *Server) issueAuthSession(w http.ResponseWriter, r *http.Request, userID string) {
	role := store.RoleUser
	if s.creds != nil && s.creds.enabled() {
		role = s.creds.role(userID)
	}
	if err := s.store.EnsureUserWithRole(r.Context(), userID, userID, role); err != nil {
		if s.log != nil {
			s.log.Warn("failed to project authenticated user", "user_id", userID, "error", err)
		}
	}
	token, err := newAuthToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	now := time.Now().UTC()
	ttl := s.cfg.AuthSessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	expiresAt := now.Add(ttl)
	if err := s.store.CreateAuthSession(r.Context(), store.AuthSession{
		TokenHash: hashAuthToken(token),
		UserID:    userID,
		CreatedAt: now,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, s.authCookie(r, token, expiresAt))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.cookieName(r)); err == nil && cookie.Value != "" {
		if err := s.store.RevokeAuthSession(r.Context(), hashAuthToken(cookie.Value), time.Now().UTC()); err != nil {
			s.log.Warn("failed to revoke auth session on logout", "error", err)
		}
	}
	http.SetCookie(w, s.expireCookie(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if !s.creds.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"auth_required": false, "user_id": labUserID, "role": store.RoleUser})
		return
	}
	userID := ""
	if cookie, err := r.Cookie(s.cookieName(r)); err == nil && cookie.Value != "" {
		if id, err := s.store.LookupAuthSession(r.Context(), hashAuthToken(cookie.Value), time.Now().UTC()); err == nil {
			userID = id
		}
	}
	role := store.RoleUser
	if userID != "" {
		role = s.creds.role(userID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"auth_required": true, "user_id": userID, "role": role})
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/sessions" && r.Method == http.MethodGet:
		s.listSessions(w, r)
	case r.URL.Path == "/api/sessions" && r.Method == http.MethodPost:
		s.createSession(w, r)
	case r.URL.Path == "/api/quota" && r.Method == http.MethodGet:
		s.getQuota(w, r)
	case r.URL.Path == "/api/deployment-capabilities" && r.Method == http.MethodGet:
		s.deploymentCapabilities(w, r)
	case r.URL.Path == "/api/feature-flags" && r.Method == http.MethodGet:
		flags, err := s.store.ListFeatureFlags(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read feature flags")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
	case r.URL.Path == "/api/events" && r.Method == http.MethodGet:
		s.events(w, r)
	case r.URL.Path == "/api/events/stream" && r.Method == http.MethodGet:
		s.eventsStream(w, r)
	case r.URL.Path == "/api/search" && r.Method == http.MethodGet:
		s.search(w, r)
	case r.URL.Path == "/api/users" && r.Method == http.MethodGet:
		s.listUsers(w, r)
	case r.URL.Path == "/api/notifications" && r.Method == http.MethodGet:
		s.listNotifications(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/notifications/") && strings.HasSuffix(r.URL.Path, "/read") && r.Method == http.MethodPost:
		s.markNotificationRead(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/sessions/"):
		s.sessionRoute(w, r)
	case r.URL.Path == "/api/schedules" && r.Method == http.MethodGet:
		s.listSchedules(w, r)
	case r.URL.Path == "/api/schedules" && r.Method == http.MethodPost:
		s.createSchedule(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/schedules/"):
		s.scheduleRoute(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) sessionRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	sessionID := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.getSession(w, r, sessionID)
		case http.MethodPatch:
			s.renameSession(w, r, sessionID)
		case http.MethodDelete:
			s.destroySession(w, r, sessionID)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	switch {
	case len(parts) == 2 && parts[1] == "messages" && r.Method == http.MethodGet:
		s.listMessages(w, r, sessionID)
	case len(parts) == 3 && parts[1] == "messages" && parts[2] == "page" && r.Method == http.MethodGet:
		s.listMessagesPage(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "runtime" && r.Method == http.MethodGet:
		s.getSessionRuntime(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "messages" && r.Method == http.MethodPost:
		s.sendMessage(w, r, sessionID)
	case len(parts) == 4 && parts[1] == "messages" && parts[3] == "feedback" && r.Method == http.MethodPost:
		s.handlePostMessageFeedback(w, r, sessionID, parts[2])
	case len(parts) == 4 && parts[1] == "messages" && (parts[3] == "content" || parts[3] == "trace-content") && r.Method == http.MethodGet:
		s.messageContent(w, r, sessionID, parts[2])
	case len(parts) == 4 && parts[1] == "messages" && parts[3] == "mentioned-files" && r.Method == http.MethodGet:
		s.mentionedFiles(w, r, sessionID, parts[2])
	case len(parts) == 4 && parts[1] == "messages" && parts[3] == "trace" && r.Method == http.MethodGet:
		s.messageTrace(w, r, sessionID, parts[2])
	case len(parts) == 3 && parts[1] == "artifacts" && (parts[2] == "children" || parts[2] == "search") && r.Method == http.MethodGet:
		s.artifactPage(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "artifacts" && r.Method == http.MethodGet:
		s.listArtifacts(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "artifacts" && r.Method == http.MethodPost:
		s.uploadArtifacts(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "interrupt" && r.Method == http.MethodPost:
		s.interruptSession(w, r, sessionID)
	case len(parts) == 3 && parts[1] == "turns" && r.Method == http.MethodDelete:
		turnID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || turnID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid turn id")
			return
		}
		s.cancelQueuedTurn(w, r, sessionID, turnID)
	case len(parts) == 4 && parts[1] == "turns" && parts[3] == "send" && r.Method == http.MethodPost:
		turnID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || turnID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid turn id")
			return
		}
		s.sendQueuedTurn(w, r, sessionID, turnID)
	case len(parts) == 2 && parts[1] == "shares" && r.Method == http.MethodPost:
		s.createSessionShare(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "shares" && r.Method == http.MethodGet:
		s.listSessionShares(w, r, sessionID)
	case len(parts) == 3 && parts[1] == "shares" && r.Method == http.MethodDelete:
		s.deleteSessionShare(w, r, sessionID, parts[2])
	case len(parts) == 2 && parts[1] == "share-links" && r.Method == http.MethodPost:
		s.createShareLink(w, r, sessionID)
	case len(parts) == 2 && parts[1] == "share-links" && r.Method == http.MethodGet:
		s.listShareLinks(w, r, sessionID)
	case len(parts) == 4 && parts[1] == "share-links" && parts[3] == "rotate" && r.Method == http.MethodPost:
		s.rotateShareLink(w, r, sessionID, parts[2])
	case len(parts) == 3 && parts[1] == "share-links" && r.Method == http.MethodDelete:
		s.deleteShareLink(w, r, sessionID, parts[2])
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// listSessions returns the caller's own sessions followed by the ones shared
// with them, each annotated with the access level so the sidebar can separate
// them and withhold rename/destroy from a shared row.
//
// ListSessionsByUser keeps its owner-only meaning: quota and the idle monitor
// read it too, so the shared half arrives through a second query instead.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	viewer := userIDFromContext(r.Context())
	sessions, err := s.store.ListSessionsByUser(r.Context(), viewer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	shared, err := s.store.ListSessionsSharedWith(r.Context(), viewer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// One query for every session's sandbox state rather than one per row; the
	// sidebar renders this alongside status so a "sleeping" session is told
	// apart from a warm one before the user pays the cold start.
	sandboxes, err := s.store.SessionSandboxStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	listings := make([]apiSharedSessionListing, 0, len(sessions)+len(shared))
	for _, session := range sessions {
		listing := ownedSessionListing(session)
		listing.Sandbox = string(sandboxes[session.ID])
		listings = append(listings, listing)
	}
	for _, entry := range shared {
		listing := sharedSessionListing(entry)
		listing.Sandbox = string(sandboxes[entry.Session.ID])
		listings = append(listings, listing)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": listings})
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	notifications, err := s.store.ListNotificationsForUser(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": publicNotifications(notifications)})
}

func (s *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/notifications/"), "/read")
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	userID := userIDFromContext(r.Context())
	if err := s.store.MarkNotificationRead(r.Context(), id, userID, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreateNotification persists a new broadcast notification and signals connected
// clients to refetch. It is invoked internally (deploy tooling, startup logic)
// and is not exposed as a public HTTP endpoint.
func (s *Server) CreateNotification(ctx context.Context, title, body string) (string, error) {
	return s.createNotification(ctx, "", title, body)
}

// createNotification persists a notification for one user ("" = everyone) and
// publishes the lightweight "notification.created" refetch signal.
//
// The signal itself is not audience-filtered, and deliberately so: its payload is
// an opaque notification id and nothing else, while the content is gated by the
// audience predicate in the list query. A user who refetches on someone else's
// directed notification simply sees no new row. Filtering event delivery per user
// would mean teaching the hub about recipients to save a refetch that costs one
// query.
func (s *Server) createNotification(ctx context.Context, userID, title, body string) (string, error) {
	id := newID("notif")
	now := time.Now().UTC()
	if err := s.store.CreateNotification(ctx, store.Notification{
		ID:        id,
		UserID:    userID,
		Title:     title,
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return "", err
	}
	s.hub.Publish(events.Event{
		Type:    "notification.created",
		Time:    now,
		Payload: map[string]any{"notification_id": id},
	})
	return id, nil
}

func (s *Server) deploymentCapabilities(w http.ResponseWriter, r *http.Request) {
	disabled := func(reason string) *string { return &reason }
	agentReason := (*string)(nil)
	agentEnabled := true
	if _, err := s.resolveModeDeployment("agent"); err != nil {
		agentEnabled = false
		agentReason = disabled(err.code)
	}
	shellReason := (*string)(nil)
	shellEnabled := true
	if _, err := s.resolveModeDeployment("shell"); err != nil {
		shellEnabled = false
		shellReason = disabled(err.code)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": 1,
		"message_pages":  true,
		"artifact_pages": true,
		"default_mode":   "agent",
		"session_modes": []modeDTO{
			{Mode: "agent", Label: "Agent", Visible: true, CreateEnabled: agentEnabled, DisabledReason: agentReason},
			{Mode: "shell", Label: "Shell", Visible: shellEnabled, CreateEnabled: shellEnabled, DisabledReason: shellReason},
		},
	})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if r.Body != nil {
		limitBody(w, r, maxControlBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	if _, ok := raw["agent"]; ok {
		writeError(w, http.StatusBadRequest, "agent input is no longer supported")
		return
	}
	mode := "agent"
	if value, ok := raw["mode"]; ok {
		if err := json.Unmarshal(value, &mode); err != nil {
			writeError(w, http.StatusBadRequest, "invalid mode")
			return
		}
	}
	mode = strings.TrimSpace(mode)
	driverID, err := s.driverForMode(mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Capacity follows retained placements and unresolved provisioning intent.
	count, err := s.store.CountSandboxOccupyingSessions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if count >= s.cfg.MaxSessions {
		writeErrorClass(w, http.StatusServiceUnavailable, "pool_exhausted", "active sandbox limit reached")
		return
	}

	id := newID("sess")
	now := time.Now().UTC()
	session := store.Session{
		ID:        id,
		UserID:    userIDFromContext(r.Context()),
		Status:    string(sessionstate.Created),
		Agent:     string(driverID),
		Mode:      mode,
		Workspace: "workspace:" + id,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Domain admission check, moved out of store.CreateSession: a bad status or
	// an unsupported driver is a bad request, not a persistence failure.
	if err := validateNewSession(session); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.CreateSession(r.Context(), session); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.Publish(events.Event{Type: "session.created", SessionID: id, Payload: publicSession(session)})
	writeJSON(w, http.StatusCreated, publicSession(session))
}

func (s *Server) driverForMode(mode string) (agents.ID, error) {
	resolution, capabilityErr := s.resolveModeDeployment(mode)
	if capabilityErr != nil {
		return "", fmt.Errorf("%s", capabilityErr.message)
	}
	return resolution.DriverID, nil
}

// authorizeSession loads a session and confirms it belongs to the caller. On a
// missing session or an owner mismatch it writes a 404 (indistinguishable, so
// callers cannot probe for sessions owned by others) and returns ok=false.
func (s *Server) authorizeSession(w http.ResponseWriter, r *http.Request, sessionID string) (store.Session, bool) {
	session, err := s.store.GetSession(r.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session not found")
		return store.Session{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return store.Session{}, false
	}
	if session.UserID != userIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "session not found")
		return store.Session{}, false
	}
	return session, true
}

// getSession is one of the four read-only routes behind the read gate rather
// than the owner gate (see resolveAccess). A reader gets the trimmed DTO.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, level, _, ok := s.authorizeRead(w, r, sessionID)
	if !ok {
		return
	}
	sandboxes, err := s.store.SessionSandboxStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var dto apiSession
	if level < accessOwner {
		dto = sharedSession(session)
	} else {
		dto = publicSession(session)
	}
	dto.Sandbox = string(sandboxes[session.ID])
	writeJSON(w, http.StatusOK, dto)
}

// maxSessionTitleRunes bounds the persisted title length. Enforced by rune
// count so multi-byte titles are not truncated mid-character.
const maxSessionTitleRunes = 200

func (s *Server) renameSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if utf8.RuneCountInString(title) > maxSessionTitleRunes {
		writeError(w, http.StatusBadRequest, "title too long")
		return
	}
	if err := s.store.UpdateSessionTitle(r.Context(), sessionID, title, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	session.Title = title
	session.TitleCustom = true
	updated := publicSession(session)
	s.hub.Publish(events.Event{Type: "session.updated", SessionID: sessionID, Payload: updated})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req struct {
		Content string `json:"content"`
	}
	limitBody(w, r, maxMessageBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	// A running session can accept additional user turns. They are persisted in
	// sequence order and the runtime drains them after the current turn, which
	// gives the UI a real queue instead of rejecting input during generation.
	if !s.ensureRunnableGenerationHTTP(w, r, session) {
		return
	}
	runningStatus := string(sessionstate.RunningActive)
	enqueue, err := s.store.EnqueueTurnMessage(r.Context(), store.EnqueueTurnMessageParams{
		SessionID: sessionID,
		Content:   req.Content,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		if strings.Contains(err.Error(), "session cannot accept input") || store.IsWorkspaceMutationBusy(err) {
			writeError(w, http.StatusConflict, "session is busy")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// No message.created here. The turn is queued, not asked: its transcript entry
	// is written when the agent starts it (see materializeTurnMessageTx), and the
	// bridge publishes the event then. Broadcasting at enqueue is what put a queued
	// prompt in the transcript and the queue simultaneously.
	s.hub.Publish(events.Event{Type: "session." + runningStatus, SessionID: sessionID})
	// The queue changed, so every client — including the sender, whose response
	// below carries the same snapshot — converges on it. The revision makes
	// applying both harmless in either order.
	s.publishSessionRuntime(sessionID, enqueue.Runtime)
	// session.Title reflects the DB value loaded at the top of this handler, so
	// an empty title means this is the first message: set an immediate
	// placeholder, then (if configured) upgrade it to an LLM-generated title
	// asynchronously so the request returns without waiting on the model.
	firstMessage := session.Title == ""
	s.maybeSetPlaceholderTitle(r.Context(), session, req.Content)
	if firstMessage {
		s.startLLMTitleGeneration(sessionID, req.Content)
	}
	// The response carries the turn id and the post-send projection, not a message:
	// the prompt has been accepted, but it becomes a transcript entry only when the
	// agent starts it. The client renders it from the queue until then, so the
	// transcript timestamp always reflects when the model actually received it.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     runningStatus,
		"session_id": sessionID,
		"turn_id":    enqueue.TurnID,
		"runtime":    enqueue.Runtime,
	})
}

// startLLMTitleGeneration kicks off asynchronous LLM title generation for a
// session's first message. It runs in its own goroutine with a detached,
// time-bounded context so it neither blocks nor is cancelled by the returning
// HTTP request. It is a no-op when the title LLM is not configured.
func (s *Server) startLLMTitleGeneration(sessionID, firstMessage string) {
	if s.titler == nil || !s.titler.Enabled() || !s.cfg.TitleLLM.Enabled {
		return
	}
	go func() {
		// Generous deadline: the title LLM may be a reasoning model that takes
		// tens of seconds. This is fully async, so a long wait costs nothing on
		// the request path; on timeout the placeholder title simply remains.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s.generateLLMTitle(ctx, sessionID, firstMessage)
	}()
}

// generateLLMTitle asks the title LLM to condense the first message into a
// short title and persists it. It is best-effort: any failure leaves the
// existing placeholder title in place and is only logged. The store update is
// guarded (custom=false) so it never overwrites a title the user set manually
// in the meantime.
func (s *Server) generateLLMTitle(ctx context.Context, sessionID, firstMessage string) {
	title, err := s.titler.GenerateTitle(ctx, firstMessage)
	if err != nil {
		s.log.Warn("llm title generation failed", "session_id", sessionID, "error", err)
		return
	}
	if err := s.store.UpdateSessionTitle(ctx, sessionID, title, false); err != nil {
		s.log.Warn("failed to persist llm session title", "session_id", sessionID, "error", err)
		return
	}
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		s.log.Warn("failed to reload session after llm title", "session_id", sessionID, "error", err)
		return
	}
	// If the user manually renamed while the model was working, the guarded
	// update above was a no-op; don't broadcast a stale title.
	if session.TitleCustom || session.Title != title {
		return
	}
	s.hub.Publish(events.Event{Type: "session.updated", SessionID: sessionID, Payload: publicSession(session)})
}

// placeholderTitleRunes bounds the auto-generated placeholder title derived
// from the first user message.
const placeholderTitleRunes = 50

// maybeSetPlaceholderTitle gives a still-untitled session an immediate,
// human-meaningful label derived from its first user message, so the sidebar
// shows something better than the raw session id right away. It is best-effort:
// failures are logged and never block message delivery. The store update is
// guarded (custom=false) so it cannot clobber a title the user set manually.
func (s *Server) maybeSetPlaceholderTitle(ctx context.Context, session store.Session, content string) {
	if strings.TrimSpace(session.Title) != "" {
		return
	}
	title := placeholderTitleFromContent(content)
	if title == "" {
		return
	}
	if err := s.store.UpdateSessionTitle(ctx, session.ID, title, false); err != nil {
		s.log.Warn("failed to set placeholder session title", "session_id", session.ID, "error", err)
		return
	}
	session.Title = title
	s.hub.Publish(events.Event{Type: "session.updated", SessionID: session.ID, Payload: publicSession(session)})
}

// placeholderTitleFromContent collapses the first user message to a single-line
// snippet of at most placeholderTitleRunes runes, appending an ellipsis when
// truncated.
func placeholderTitleFromContent(content string) string {
	collapsed := strings.Join(strings.Fields(content), " ")
	if collapsed == "" {
		return ""
	}
	if utf8.RuneCountInString(collapsed) <= placeholderTitleRunes {
		return collapsed
	}
	runes := []rune(collapsed)
	return strings.TrimRight(string(runes[:placeholderTitleRunes]), " ") + "…"
}

// The session runtime projection is served two ways — this endpoint and the
// `session.runtime` event — carrying the identical payload, so a client has one
// shape to apply however it arrives. Everything it needs to answer "is the agent
// busy, what is it doing, what is queued" is here; nothing has to be inferred
// from the coarse session status.
func (s *Server) getSessionRuntime(w http.ResponseWriter, r *http.Request, sessionID string) {
	if _, _, _, ok := s.authorizeRead(w, r, sessionID); !ok {
		return
	}
	snapshot, err := s.store.GetSessionRuntimeSnapshot(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// publishSessionRuntime pushes a projection that a store call already produced
// inside its transaction. Live-only: the turns table is the replay source, and a
// reconnecting client re-reads the endpoint, so there is nothing to persist.
func (s *Server) publishSessionRuntime(sessionID string, snapshot store.SessionRuntimeSnapshot) {
	s.hub.Publish(events.Event{Type: "session.runtime", SessionID: sessionID, Payload: snapshot})
}

// publishSessionRuntimeFromStore is publishSessionRuntime for callers that
// changed turn state without obtaining a snapshot (the bridge completion path).
// Best effort: a failed read only costs this one push, and the client's next
// event or reconnect re-reads the endpoint.
func (s *Server) publishSessionRuntimeFromStore(ctx context.Context, sessionID string) {
	snapshot, err := s.store.GetSessionRuntimeSnapshot(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("failed to read session runtime projection", "session_id", sessionID, "error", err)
		}
		return
	}
	s.publishSessionRuntime(sessionID, snapshot)
}

// cancelQueuedTurn withdraws a turn the user queued but no longer wants.
//
// A real server operation rather than a client-side list edit: the turn is
// durable and the worker will run it, so removing it only from the sender's view
// meant the prompt still executed minutes later.
func (s *Server) cancelQueuedTurn(w http.ResponseWriter, r *http.Request, sessionID string, turnID int64) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	snapshot, err := s.store.CancelQueuedTurn(r.Context(), store.CancelQueuedTurnParams{
		SessionID: sessionID,
		TurnID:    turnID,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		// The worker claimed it first. An expected race, not a fault: the turn is
		// running now and Stop is the way to end it.
		if errors.Is(err, store.ErrTurnNotCancelable) {
			writeErrorClass(w, http.StatusConflict, "turn_not_cancelable", "turn is no longer queued")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishSessionRuntime(sessionID, snapshot)
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, sessionID string) {
	_, level, visibility, ok := s.authorizeRead(w, r, sessionID)
	if !ok {
		return
	}
	userID := userIDFromContext(r.Context())
	messages, err := s.store.ListMessagesForUser(r.Context(), sessionID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if level < accessOwner {
		messages = sharedMessages(messages, visibility)
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

// handlePostMessageFeedback records or clears the authenticated user's rating
// ("up"/"down"; null/"" clears) for a single message. The message must belong
// to the session in the URL, and that session must be owned by the caller.
func (s *Server) handlePostMessageFeedback(w http.ResponseWriter, r *http.Request, sessionID, messageIDStr string) {
	if _, ok := s.authorizeSession(w, r, sessionID); !ok {
		return
	}
	messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}
	var req struct {
		Rating *string `json:"rating"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	msgSessionID, ok, err := s.store.MessageSession(r.Context(), messageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || msgSessionID != sessionID {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	rating := ""
	if req.Rating != nil {
		rating = strings.TrimSpace(*req.Rating)
	}
	switch rating {
	case "", "none", "up", "down":
	default:
		writeError(w, http.StatusBadRequest, "invalid feedback rating")
		return
	}
	userID := userIDFromContext(r.Context())
	effective, err := s.store.SetMessageFeedback(r.Context(), messageID, userID, rating)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": effective})
}

const searchDefaultLimit = 50

// search runs a full-text search over the caller's session titles and message
// bodies. Results are always scoped to the authenticated user by the store.
// The `q` query param is the search text; `limit` is optional and clamped to
// [1, searchDefaultLimit]. truncated is true when the result set was capped at
// the limit (more matches may exist).
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	limit := searchDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit must be an integer")
			return
		}
		limit = parsed
	}
	if limit < 1 {
		limit = 1
	}
	if limit > searchDefaultLimit {
		limit = searchDefaultLimit
	}

	hits, err := s.store.SearchMessages(r.Context(), userIDFromContext(r.Context()), query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":   publicSearchHits(hits),
		"truncated": len(hits) == limit,
	})
}

func (s *Server) interruptSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	// running_idle is accepted alongside running_active. Stop is offered from the
	// coarse session status until the first runtime projection arrives, so a press
	// against an already-idle session is reachable and routine; the store makes it
	// a no-op rather than an error.
	if session.Status != string(sessionstate.RunningActive) && session.Status != string(sessionstate.RunningIdle) {
		writeError(w, http.StatusConflict, "session is not running")
		return
	}

	commands, ok := s.executions.(interface {
		DeliverTurnCommand(context.Context, execution.TurnCommand) error
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "execution control unavailable")
		return
	}
	command, snapshot, err := s.store.RequestBoundInterrupt(r.Context(), sessionID, session.ActiveGenerationID, store.GenerationLeaseOwner(s.ownerUUID))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.publishSessionRuntime(sessionID, snapshot)
	if command != nil {
		if err := commands.DeliverTurnCommand(r.Context(), *command); err != nil {
			writeErrorClass(w, http.StatusServiceUnavailable, "control_delivery_pending", "interrupt delivery is pending reconciliation")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "interrupting", "runtime": snapshot})
	return
}

func (s *Server) destroySession(w http.ResponseWriter, r *http.Request, sessionID string) {
	_, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}

	retirement, ok := s.executions.(interface {
		BoundedRetireSession(context.Context, string, string) (int64, error)
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "session retirement unavailable")
		return
	}
	id, err := retirement.BoundedRetireSession(r.Context(), sessionID, store.GenerationLeaseOwner(s.ownerUUID))
	if err != nil {
		writeErrorClass(w, http.StatusServiceUnavailable, "session_retirement_pending", "session retirement is awaiting reconciliation")
		return
	}
	s.publishDurableEvent(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "destroyed"})
	return
}

// listArtifacts lists the whole verified workspace. An identity share exposes
// that full set by design (see the proposal's trace-vs-artifact section); the
// per-file whitelist exists only for anonymous links.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request, sessionID string) {
	if _, _, _, ok := s.authorizeRead(w, r, sessionID); !ok {
		return
	}
	if !s.refreshRetainedArtifacts(w, r, sessionID) {
		return
	}
	items, err := s.store.ListArtifacts(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": items})
}

// artifactFile serves the /artifacts/{sessionID}/{path} endpoint. GET downloads
// the file; PUT overwrites it for the online editor. The mux registration
// matches all methods, so we dispatch here.
func (s *Server) artifactFile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getArtifact(w, r)
	case http.MethodPut:
		s.writeArtifact(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/artifacts/"), "/", 2)
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, "invalid artifact path")
		return
	}
	// Syntactic rejection first, so a malformed path costs no session lookup and
	// leaks nothing about which sessions exist.
	if _, status, message := validateArtifactRequestPath(parts[0], parts[1]); status != 0 {
		writeError(w, status, message)
		return
	}
	// The read gate runs here, in the handler, and its result is passed down.
	// openArtifactFile no longer decides for itself who may read: it is shared
	// with the PUT path, so relaxing its internal owner comparison would have
	// relaxed writes too.
	_, level, _, ok := s.authorizeRead(w, r, parts[0])
	if !ok {
		return
	}
	file, info, status, message := s.openArtifactFile(r.Context(), parts[0], parts[1], level)
	if file == nil {
		writeError(w, status, message)
		return
	}
	defer file.Close()

	// Harden artifact responses: never sniff into active content.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// HTML artifacts get a preview in the workbench, which frames them from the
	// app's own origin. Blanket DENY / `sandbox` (which we still use for every
	// other type) makes that iframe fail to load, so HTML gets a policy that is
	// framable same-origin AND renders, while still cutting off every exfiltration
	// path: no outbound connect/img/font to other hosts, no forms, no nested
	// frames or plugins. Inline script/style are allowed so self-contained pages
	// render, but they can only touch same-origin resources and cannot phone home.
	ext := strings.ToLower(filepath.Ext(info.Path))
	switch ext {
	case ".html", ".htm":
		s.setPreviewFraming(w, []string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"font-src 'self' data:",
			"media-src 'self' data: blob:",
			"connect-src 'none'",
			"form-action 'none'",
			"frame-src 'none'",
			"object-src 'none'",
			"base-uri 'none'",
		})
	case ".pdf":
		// PDFs are previewed in a same-origin iframe in the workbench. A blanket
		// DENY (or the `sandbox` CSP below) makes the browser refuse to frame the
		// file, which surfaces as a bogus "<host> refused to connect" error. PDF
		// is passive content rendered by the browser's built-in viewer, so allow
		// same-origin framing while still forbidding any active/outbound behaviour.
		s.setPreviewFraming(w, []string{
			"default-src 'none'",
			"img-src 'self' data: blob:",
			"object-src 'self'",
			"plugin-types application/pdf",
		})
	default:
		// Everything else: inert. Forbid script execution and framing entirely if
		// a browser opens one directly.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("X-Frame-Options", "DENY")
	}
	// http.ServeContent sets Last-Modified from the workspace metadata; the online
	// editor reads it and echoes it back in If-Unmodified-Since on PUT so we can
	// detect a stale edit (the agent changed the file since the editor loaded).
	http.ServeContent(w, r, filepath.Base(info.Path), info.ModifiedAt, file)
}

// openArtifactFile opens one file under a session's verified live workspace.
//
// granted is the access level the CALLER already established, passed in
// explicitly rather than re-derived here. This function is shared by the read
// path (artifact GET), the online editor's optimistic-concurrency probe, and the
// workspace download, so widening its notion of "may read" in place would widen
// the write paths with it. Read callers pass the read gate's result; write
// callers pass accessOwner. Forgetting to pass anything is a compile error,
// which is stronger than the previous arrangement where PUT was safe only
// because authorizeSession happened to run first.
// validateArtifactRequestPath applies the syntactic hardening shared by every
// artifact read path and returns the cleaned relative path. It touches no state,
// so a malformed request is rejected with a 400 before any session lookup --
// preserving the property that a bad path says nothing about whether the session
// exists.
func validateArtifactRequestPath(sessionID, artifactPath string) (string, int, string) {
	if !safePathSegment(sessionID) {
		return "", http.StatusBadRequest, "invalid artifact path"
	}
	clean, err := workspace.CleanPath(artifactPath)
	if err != nil {
		return "", http.StatusBadRequest, "invalid artifact path"
	}
	return clean, 0, ""
}

func (s *Server) openArtifactFile(ctx context.Context, sessionID, artifactPath string, granted accessLevel) (io.ReadSeekCloser, workspace.File, int, string) {
	if granted < accessReader {
		return nil, workspace.File{}, http.StatusNotFound, "artifact not found"
	}
	cleanPath, status, message := validateArtifactRequestPath(sessionID, artifactPath)
	if status != 0 {
		return nil, workspace.File{}, status, message
	}

	// Access was decided by the caller (see granted). Confirm the session row
	// still exists before exposing any file metadata, so a deleted session cannot
	// be probed through a stale grant.
	if _, err := s.store.GetSession(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
		return nil, workspace.File{}, http.StatusNotFound, "artifact not found"
	} else if err != nil {
		return nil, workspace.File{}, http.StatusInternalServerError, err.Error()
	}

	if s.workspaceReads == nil {
		return nil, workspace.File{}, http.StatusServiceUnavailable, "workspace backend unavailable"
	}
	file, info, err := s.workspaceReads.OpenSession(ctx, sessionID, store.GenerationLeaseOwner(s.ownerUUID), cleanPath)
	return artifactFileResult(file, info, err)
}

func artifactFileResult(file io.ReadSeekCloser, info workspace.File, err error) (io.ReadSeekCloser, workspace.File, int, string) {
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist), errors.Is(err, sql.ErrNoRows):
			return nil, workspace.File{}, http.StatusNotFound, "artifact not found"
		case errors.Is(err, workspace.ErrForbidden), errors.Is(err, os.ErrPermission):
			return nil, workspace.File{}, http.StatusForbidden, "artifact path is not a regular file or directory"
		case errors.Is(err, workspace.ErrMutationBusy), errors.Is(err, execution.ErrBusy):
			return nil, workspace.File{}, http.StatusConflict, "workspace is changing state"
		case sandbox.Is(err, sandbox.Unavailable), sandbox.Is(err, sandbox.Precondition), sandbox.Is(err, sandbox.IdentityChanged):
			return nil, workspace.File{}, http.StatusServiceUnavailable, "workspace backend unavailable"
		default:
			return nil, workspace.File{}, http.StatusInternalServerError, err.Error()
		}
	}
	return file, info, 0, ""
}

func safePathSegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." && !strings.Contains(segment, "/")
}

const (
	maxControlBodyBytes = 64 << 10 // 64 KiB
	maxMessageBodyBytes = 1 << 20  // 1 MiB
)

// limitBody wraps r.Body with a MaxBytesReader so decoding fails cleanly with a
// 400 (via http.MaxBytesError) once the cap is exceeded, instead of reading an
// unbounded amount into memory.
func limitBody(w http.ResponseWriter, r *http.Request, max int64) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, max)
	}
}

// decodeJSONBody caps the request body at max bytes and decodes it into dst.
// On decode failure it writes a 400 "invalid json" response and returns false,
// signalling the caller to return without further processing.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, max int64, dst any) bool {
	limitBody(w, r, max)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return false
	}
	return true
}

func newID(prefix string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeErrorClass(w http.ResponseWriter, status int, class, message string) {
	writeJSON(w, status, map[string]string{"error_class": class, "error": message})
}

func (s *Server) SetExecutionController(controller sessionExecutionController) error {
	if controller == nil {
		return fmt.Errorf("Khora execution controller required")
	}
	s.executions = controller
	return nil
}
func (s *Server) scanSessionArtifacts(ctx context.Context, session string) error {
	if s.workspaceInventory == nil {
		return fmt.Errorf("workspace inventory unavailable")
	}
	return s.workspaceInventory.ScanSession(ctx, session)
}
func (s *Server) refreshRetainedArtifacts(w http.ResponseWriter, r *http.Request, session string) bool {
	if err := s.scanSessionArtifacts(r.Context(), session); err != nil {
		writeErrorClass(w, http.StatusServiceUnavailable, "artifact_scan_pending", "workspace inventory is reconciling")
		return false
	}
	return true
}
func (s *Server) getQuota(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.CountSandboxOccupyingSessions(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"soft_session_ceiling": s.cfg.MaxSessions, "active_sessions": count})
}
