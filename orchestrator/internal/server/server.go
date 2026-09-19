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
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/artifacts"
	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/deploymentgate"
	"harness-platform/orchestrator/internal/driverstate"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/memory"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/titler"
	"harness-platform/orchestrator/internal/workspace"
	localworkspace "harness-platform/orchestrator/internal/workspace/local"
)

const labUserID = "lab"

const (
	idleCheckpointInterval  = 5 * time.Minute
	idleCheckpointThreshold = 30 * time.Minute
	checkpointTimeout       = 2 * time.Minute
	proxyCorrelationSocket  = "proxy-correlation.sock"
)

var ()

type generationStartFailureMode int

const (
	startFailureInputAcceptable generationStartFailureMode = iota
	startFailureInputBlocking
)

type Server struct {
	executions           sessionExecutionController
	newSessionBackend    string
	nativeStateValidator func(context.Context, store.DriverStateValidationInput, int64) (workspace.File, error)
	cfg                  config.Config
	store                serverStore
	runtime              runtimeDriver
	watcher              *artifacts.Watcher
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

	// kb/kbMemories back the per-entry KB review endpoints. Nil kb means the
	// deployment has no knowledge base configured and those routes report 503.
	kb         *memory.KB
	kbMemories kbReviewStore

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

	// cleanupFailures counts consecutive teardown failures per generation, so a
	// generation that can never be reclaimed escalates instead of retrying in
	// silence. The 2026-08-19 outage logged 1343 identical WARNs over 22h and
	// nothing ever raised the severity; see runtimeCleanupFailureAlertThreshold.
	cleanupFailureMu sync.Mutex
	cleanupFailures  map[string]int

	// memoryPressureWarned rate-limits the per-generation memory-pressure notice.
	memoryPressureMu     sync.Mutex
	memoryPressureWarned map[string]time.Time
	schedulerWake        chan struct{}
	schedulerRunsMu      sync.Mutex
	schedulerRunsActive  map[string]struct{}

	// shutdownCh is closed before the HTTP servers begin graceful shutdown. Long
	// lived streams (SSE/WebSocket) select on it because net/http.Server.Shutdown
	// does not cancel active request contexts; without this signal they can keep
	// Shutdown waiting until its deadline expires.
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
}

type sessionExecutionController interface {
	Ensure(context.Context, execution.Session, string) (execution.Binding, error)
	ReleaseIdle(context.Context, string, string, string) error
}

// SetExecutionController configures the Khora execution port during startup.
// Creation policy affects new rows only; dispatch reads each persisted backend.
func (s *Server) SetExecutionController(controller sessionExecutionController, newSessionBackend string) error {
	if newSessionBackend != "legacy" && newSessionBackend != "khora" {
		return fmt.Errorf("explicit new-session backend required")
	}
	if newSessionBackend == "khora" && controller == nil {
		return fmt.Errorf("Khora creation requires an execution controller")
	}
	s.executions = controller
	s.newSessionBackend = newSessionBackend
	return nil
}

type sessionOwnerEntry struct {
	userID    string
	expiresAt time.Time
}

type userContextKey struct{}

type runtimeDriver interface {
	Start(context.Context, runtime.StartRequest, func(runtime.Output)) runtime.Result
	PrepareGeneration(context.Context, runtime.StartRequest) (runtime.GenerationArtifacts, error)
	Destroy(context.Context, string) error
	DestroyGenerationResources(context.Context, store.RuntimeGenerationDetails) (runtime.GenerationResourceCleanup, error)
	Interrupt(string) error
	// SandboxRemnantsGone reports whether every host process for a container is
	// gone, gating release of a generation whose teardown failed.
	SandboxRemnantsGone(context.Context, string) (bool, error)
	// SandboxOOMKilled reports whether the sandbox's memcg recorded an OOM kill.
	// ok=false means unknown, not "no".
	SandboxOOMKilled(context.Context, string) (killed bool, ok bool)
	// SandboxMemoryUsage reports current memcg usage and ceiling, for warning an
	// agent before the kernel kills it. ok=false means unavailable.
	SandboxMemoryUsage(context.Context, string) (used int64, limit int64, ok bool)
}

// bridgeStore is the bridge-facing slice of the store. It is kept as a named
// role (rather than folded into serverStore's body) because maintenance.go and
// bridge_startup.go hand exactly this subset to internal/bridge. The role
// interfaces live in store_iface.go; serverStore embeds this one.
type bridgeStore interface {
	bridge.Store
	ListBridgePollGenerations(context.Context, string, time.Time, time.Duration) ([]store.BridgePollGeneration, error)
}

type ensuredGeneration struct {
	Backend               string
	Allocation            store.GenerationAllocation
	IsNew                 bool
	RestoreFromCheckpoint bool
}

type bridgeStreamParserKey struct {
	SessionID    string
	GenerationID string
	TurnID       int64
}

type proxyPeerCredentials struct {
	UID int
	GID int
	PID int
}

type proxyPeerCredentialsResult struct {
	Credentials proxyPeerCredentials
	Err         error
}

type proxyPeerCredentialsContextKey struct{}

func New(
	cfg config.Config,
	store serverStore,
	runtime runtimeDriver,
	watcher *artifacts.Watcher,
	hub *events.Hub,
	log *slog.Logger,
) *Server {
	return &Server{
		cfg:     cfg,
		store:   store,
		runtime: runtime,
		watcher: watcher,
		hub:     hub,
		log:     log,
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
		if deploymentgate.Draining() && r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusServiceUnavailable, "deployment draining; retry after maintenance")
			return
		}
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

func (s *Server) ProxyCorrelationRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /internal/proxy/requests/start", s.requireProxyPeerCredentials(http.HandlerFunc(s.internalProxyRequestStart)))
	mux.Handle("POST /internal/proxy/requests/finish", s.requireProxyPeerCredentials(http.HandlerFunc(s.internalProxyRequestFinish)))
	// Data-plane gateways (Doris, OBS) authorize here. Same SO_PEERCRED gate as
	// the model-proxy routes: only a process running as the proxy service
	// identity may ask, so a sandbox cannot reach this even though it can reach
	// the gateway.
	mux.Handle("POST /internal/dataplane/requests/authorize", s.requireProxyPeerCredentials(http.HandlerFunc(s.internalDataProxyAuthorize)))
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

func (s *Server) ProxyCorrelationServer() *http.Server {
	return &http.Server{
		Handler:           s.ProxyCorrelationRoutes(),
		ReadHeaderTimeout: 5 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			credentials, err := unixPeerCredentials(conn)
			return context.WithValue(ctx, proxyPeerCredentialsContextKey{}, proxyPeerCredentialsResult{
				Credentials: credentials,
				Err:         err,
			})
		},
	}
}

func (s *Server) ListenProxyCorrelation() (net.Listener, string, error) {
	roots, err := config.ValidateIsolationRoots(s.cfg.IsolationRoots())
	if err != nil {
		return nil, "", err
	}
	socketPath := filepath.Join(roots.ProxyInternalRoot, proxyCorrelationSocket)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return nil, "", fmt.Errorf("create proxy correlation socket root: %w", err)
	}
	if err := chownProxyCorrelationPath(filepath.Dir(socketPath), s.cfg.Harness.ProxyServiceIdentity.GID); err != nil {
		return nil, "", fmt.Errorf("chown proxy correlation socket root: %w", err)
	}
	if err := os.Chmod(filepath.Dir(socketPath), 0o750); err != nil {
		return nil, "", fmt.Errorf("chmod proxy correlation socket root: %w", err)
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode().Type()&os.ModeSocket == 0 {
			return nil, "", fmt.Errorf("proxy correlation socket path %q exists and is not a socket", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, "", fmt.Errorf("remove stale proxy correlation socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("stat proxy correlation socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, "", fmt.Errorf("listen proxy correlation socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		return nil, "", fmt.Errorf("chmod proxy correlation socket: %w", err)
	}
	if err := chownProxyCorrelationPath(socketPath, s.cfg.Harness.ProxyServiceIdentity.GID); err != nil {
		_ = listener.Close()
		return nil, "", fmt.Errorf("chown proxy correlation socket: %w", err)
	}
	return listener, socketPath, nil
}

func chownProxyCorrelationPath(path string, proxyServiceGID int) error {
	if os.Geteuid() != 0 {
		return nil
	}
	if proxyServiceGID < 0 {
		return fmt.Errorf("proxy service gid must be >= 0")
	}
	return os.Chown(path, 0, proxyServiceGID)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version, "commit": s.buildCommit, "draining": deploymentgate.Draining(), "background_held": deploymentgate.BackgroundHeld(), "background_active": deploymentgate.Active()})
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
		ExpiresAt: expiresAt,
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
	case strings.HasPrefix(r.URL.Path, "/api/kb-review"):
		s.kbReviewRoute(w, r)
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

func (s *Server) getQuota(w http.ResponseWriter, r *http.Request) {
	// Report the same count the create-gate enforces: sessions holding a live
	// sandbox, not every non-terminal session.
	activeSessions, err := s.store.CountSandboxOccupyingSessions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resourceQuota, err := s.store.GetResourceQuota(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	poolCeiling := cidrPool30Capacity(s.cfg.Harness.Network.CIDRPool.Prefix)
	remainingPoolSlots := poolCeiling - resourceQuota.AllocatedPoolSlots
	if remainingPoolSlots < 0 {
		remainingPoolSlots = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"soft_session_ceiling": s.cfg.MaxSessions,
		"active_sessions":      activeSessions,
		"live_pool_ceiling":    poolCeiling,
		"allocated_pool_slots": resourceQuota.AllocatedPoolSlots,
		"remaining_pool_slots": remainingPoolSlots,
	})
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
	// The ceiling limits concurrent sandboxes, not total sessions. Idle and
	// checkpointed sessions have released their sandbox compute (their
	// resources are reclaimed or reserved_checkpointed) and must not count,
	// otherwise auto-checkpoint frees a sandbox but never frees a slot.
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
	var expiresAt *time.Time
	if s.cfg.SessionRetention > 0 {
		value := now.Add(s.cfg.SessionRetention)
		expiresAt = &value
	}
	session := store.Session{
		ExecutionBackend:      s.newSessionBackend,
		ID:                    id,
		UserID:                userIDFromContext(r.Context()),
		Status:                string(sessionstate.Created),
		Agent:                 string(driverID),
		Mode:                  mode,
		Workspace:             filepath.Join(s.cfg.SessionsRoot, id),
		RestoreID:             "unused-" + id,
		AutoCheckpointEnabled: s.cfg.Harness.Checkpoint.AutoEnabled,
		CreatedAt:             now,
		UpdatedAt:             now,
		ExpiresAt:             expiresAt,
	}
	if s.newSessionBackend == "khora" {
		session.Workspace = "workspace:" + id
		session.AutoCheckpointEnabled = false
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
	if _, err := s.store.SweepExpiredSessions(r.Context(), time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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

func (s *Server) resourceAllocatorConfig(agent string) store.ResourceAllocatorConfig {
	if driverID, err := agents.CanonicalDriverID(agent); err == nil {
		agent = string(driverID)
	}
	driverSpec, ok := agents.DriverSpecFor(agent)
	if !ok {
		driverSpec = agents.DriverSpec{ID: agents.ID(agent), OutputFormat: "stream-json"}
	}
	model := "sonnet"
	disableNonessentialTraffic := true
	if _, agentCfg, ok := s.enabledAgentConfigForDriver(agents.ID(agent)); ok {
		if agentCfg.DisableNonessentialTraffic != nil {
			disableNonessentialTraffic = *agentCfg.DisableNonessentialTraffic
		}
		if strings.TrimSpace(agentCfg.ModelProfile) != "" {
			if profile, ok := s.cfg.DeploymentModelProfiles()[agentCfg.ModelProfile]; ok && strings.TrimSpace(profile.Model) != "" {
				model = strings.TrimSpace(profile.Model)
			}
		}
	}
	return store.ResourceAllocatorConfig{
		RunDir:                      s.cfg.Harness.RunDir,
		CIDRPool:                    s.cfg.Harness.Network.CIDRPool.Prefix,
		EgressDorisFEHosts:          s.cfg.Harness.Network.Egress.DorisFEHosts,
		EgressDorisBEHosts:          s.cfg.Harness.Network.Egress.DorisBEHosts,
		EgressDorisPorts:            s.cfg.Harness.Network.Egress.DorisPorts,
		EgressDNSPolicy:             string(s.cfg.Harness.Network.Egress.DNSPolicy),
		EgressAllowAll:              s.cfg.Harness.Network.Egress.AllowAll,
		HostProxyBindURL:            s.cfg.ModelProxy.BindURL,
		ProxyPort:                   s.cfg.ModelProxy.BindPort,
		Agent:                       agent,
		AgentModel:                  model,
		AgentOutputFormat:           driverSpec.OutputFormat,
		DisableNonessentialTraffic:  disableNonessentialTraffic,
		SandboxUID:                  s.cfg.Harness.SandboxIdentity.UID,
		SandboxGID:                  s.cfg.Harness.SandboxIdentity.GID,
		SandboxSupplementalGIDs:     s.cfg.Harness.SandboxIdentity.SupplementalGIDs,
		ProviderCredentialsHostOnly: driverSpec.ModelAccess,
		SandboxModelProxyBaseURL:    s.cfg.ModelProxy.SandboxBaseURL,
	}
}

func cidrPool30Capacity(prefix netip.Prefix) int {
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() > 30 {
		return 0
	}
	return 1 << uint(30-prefix.Bits())
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

type sessionRuntimeDataVolumes struct {
	Workspace  store.SessionWorkspaceVolume
	DriverHome store.SessionDriverHomeVolume
}

func (s *Server) runtimeStartRequest(session store.Session, generationID string, details store.RuntimeGenerationDetails, artifacts runtime.GenerationArtifacts, volumes sessionRuntimeDataVolumes) runtime.StartRequest {
	return runtime.StartRequest{
		SessionID:         session.ID,
		GenerationID:      generationID,
		Agent:             session.Agent,
		WaitForTurn:       false,
		Generation:        details,
		PreparedArtifacts: artifacts,
		WorkspaceHostPath: volumes.Workspace.HostPath,
		AgentHomeHostPath: volumes.DriverHome.HostPath,
	}
}

func validateDriverStateForRuntimeLaunch(details store.RuntimeGenerationDetails, volumes sessionRuntimeDataVolumes) error {
	_, err := driverstate.ValidateLocal(context.Background(), details.Agent, details.DriverStatePayload, volumes.DriverHome.HostPath, "")
	return err
}

func (s *Server) ensureSessionRuntimeDataVolumes(ctx context.Context, session store.Session) (sessionRuntimeDataVolumes, error) {
	volumeConfig, err := s.dataVolumeProvisionerConfig()
	if err != nil {
		return sessionRuntimeDataVolumes{}, err
	}
	now := time.Now().UTC()
	workspace, err := (localworkspace.Volumes{Records: s.store}).ProvisionSessionWorkspace(ctx, localworkspace.ProvisionSessionWorkspaceParams{
		SessionID: session.ID,
		Config:    volumeConfig,
		Now:       now,
	})
	if err != nil {
		return sessionRuntimeDataVolumes{}, fmt.Errorf("provision session workspace volume: %w", err)
	}
	driverHome, err := (localworkspace.Volumes{Records: s.store}).ProvisionSessionDriverHome(ctx, localworkspace.ProvisionSessionDriverHomeParams{
		SessionID: session.ID,
		Driver:    session.Agent,
		Config:    volumeConfig,
		Now:       now,
	})
	if err != nil {
		return sessionRuntimeDataVolumes{}, fmt.Errorf("provision session driver home volume: %w", err)
	}
	return sessionRuntimeDataVolumes{Workspace: workspace, DriverHome: driverHome}, nil
}

func (s *Server) destroyGenerationRuntime(ctx context.Context, details store.RuntimeGenerationDetails) error {
	runtimeID := strings.TrimSpace(details.RunscContainerID)
	if runtimeID == "" {
		return fmt.Errorf("generation %s has no runsc container id", details.GenerationID)
	}
	return s.runtime.Destroy(ctx, runtimeID)
}

func sandboxContractID(generationID string) string {
	return "contract_" + strings.TrimSpace(generationID)
}

func driverConfigMaterializationPayload(driverID string, entries []runtime.DriverConfigMaterialization) (map[string]any, map[string]any, error) {
	driverID = strings.TrimSpace(driverID)
	specs := agents.DriverConfigMaterializationSpecsFor(agents.ID(driverID))
	if len(specs) == 0 {
		if len(entries) != 0 {
			return nil, nil, fmt.Errorf("driver %s does not support driver config materialization", driverID)
		}
		return map[string]any{}, nil, nil
	}
	if len(entries) != len(specs) {
		return nil, nil, fmt.Errorf("driver %s config materialization requires %d projections", driverID, len(specs))
	}
	runtimePayload := map[string]any{}
	mountPayload := map[string]any{}
	expected := map[string]agents.DriverConfigMaterializationSpec{}
	for _, spec := range specs {
		expected[spec.Name] = spec
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		want, ok := expected[name]
		if !ok {
			return nil, nil, fmt.Errorf("unsupported %s driver config materialization %q", driverID, entry.Name)
		}
		if _, ok := seen[name]; ok {
			return nil, nil, fmt.Errorf("duplicate %s driver config materialization %q", driverID, name)
		}
		seen[name] = struct{}{}
		if entry.SourceProjectionPath != want.SourceProjectionPath || entry.SandboxDestination != want.SandboxDestination {
			return nil, nil, fmt.Errorf("%s driver config materialization %s path mismatch", driverID, name)
		}
		if !strings.HasPrefix(strings.TrimSpace(entry.SourceDigest), "sha256:") {
			return nil, nil, fmt.Errorf("%s driver config materialization %s digest is required", driverID, name)
		}
		if entry.DestinationMutableBySandbox != want.DestinationMutableBySandbox {
			return nil, nil, fmt.Errorf("%s driver config materialization %s mutability mismatch", driverID, name)
		}
		runtimePayload[name] = map[string]any{
			"source_projection_path":         entry.SourceProjectionPath,
			"source_digest":                  entry.SourceDigest,
			"sandbox_destination":            entry.SandboxDestination,
			"destination_mutable_by_sandbox": entry.DestinationMutableBySandbox,
		}
		mountPayload[name] = map[string]any{
			"type":                           want.MountType,
			"mode":                           want.MountMode,
			"exact":                          want.MountExact,
			"source_projection_path":         entry.SourceProjectionPath,
			"sandbox_destination":            entry.SandboxDestination,
			"destination_mutable_by_sandbox": entry.DestinationMutableBySandbox,
		}
	}
	if len(seen) != len(expected) {
		return nil, nil, fmt.Errorf("%s driver config materialization missing required projections", driverID)
	}
	return runtimePayload, mountPayload, nil
}

func runtimeResourcePostStartProof(instance store.RuntimeResourceInstance, result runtime.Result, bridgeStartupEvidence string) (store.RuntimeResourcePostStartProof, error) {
	if result.PostStartProof == nil {
		return store.RuntimeResourcePostStartProof{}, fmt.Errorf("runtime start did not return post-start proof for generation %s", instance.GenerationID)
	}
	proof := *result.PostStartProof
	proof.HostID = instance.HostID
	proof.ContractID = instance.ContractID
	proof.SandboxContractVersion = instance.SandboxContractVersion
	proof.BridgeStartup = strings.TrimSpace(bridgeStartupEvidence)
	if strings.TrimSpace(proof.GenerationID) == "" {
		proof.GenerationID = instance.GenerationID
	}
	if strings.TrimSpace(proof.RunscContainerID) == "" {
		proof.RunscContainerID = instance.RunscContainerID
	}
	return proof, nil
}

func runtimeResourceCleanupEvidence(instance store.RuntimeResourceInstance, cleanup runtime.GenerationResourceCleanup) store.ResourceReconciliationEvidence {
	filesystem := make(map[string]string, len(cleanup.FilesystemLstat))
	for path, value := range cleanup.FilesystemLstat {
		filesystem[path] = value
	}
	return store.ResourceReconciliationEvidence{
		HostID:          instance.HostID,
		RunscState:      cleanup.RunscState,
		IPNetns:         cleanup.IPNetns,
		IPLink:          cleanup.IPLink,
		NFT:             cleanup.NFT,
		FilesystemLstat: filesystem,
	}
}

func runtimeResourceWorkerID(ownerUUID, leaseOwner string) string {
	workerID := strings.TrimSpace(ownerUUID)
	if workerID != "" {
		return workerID
	}
	leaseOwner = strings.TrimSpace(leaseOwner)
	suffix := ":" + store.RuntimeManagerRoleTag
	if strings.HasSuffix(leaseOwner, suffix) {
		workerID = strings.TrimSpace(strings.TrimSuffix(leaseOwner, suffix))
	}
	if workerID == "" {
		workerID = leaseOwner
	}
	return workerID
}

func runtimeResourceHostID() string {
	host, err := os.Hostname()
	if err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	return "unknown-host"
}

func runtimeResourceSandboxIP(cidr string) (string, error) {
	cidr = strings.TrimSpace(cidr)
	prefix, err := netip.ParsePrefix(cidr)
	if err == nil {
		return prefix.Addr().String(), nil
	}
	if before, _, ok := strings.Cut(cidr, "/"); ok && strings.TrimSpace(before) != "" {
		return strings.TrimSpace(before), nil
	}
	return "", fmt.Errorf("runtime resource sandbox ip cidr %q is invalid: %w", cidr, err)
}

func runtimeResourceNftTableName(generationID string) string {
	return "harness_gen_" + runtimeResourceIdentifier(generationID)
}

func runtimeResourceIdentifier(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

func (s *Server) runtimeResourceRootPrefixes() map[string]string {
	roots := s.cfg.IsolationRoots()
	if strings.TrimSpace(s.cfg.DBPath) == "" {
		roots.DataVolumeEvidenceRoot = ""
	}
	if strings.TrimSpace(s.cfg.Harness.RunDir) == "" {
		roots.ProxyInternalRoot = ""
	}
	values := map[string]string{
		"sessions_root":             roots.SessionsRoot,
		"agent_homes_root":          roots.AgentHomesRoot,
		"run_dir":                   roots.RunDir,
		"checkpoints_root":          roots.CheckpointsRoot,
		"prepared_bundle_root":      roots.PreparedBundleRoot,
		"rootfs_path":               roots.RootFSPath,
		"db_path":                   roots.DBPath,
		"data_volume_evidence_root": roots.DataVolumeEvidenceRoot,
		"proxy_internal_root":       roots.ProxyInternalRoot,
		"provider_credential_root":  roots.ProviderCredentialRoot,
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = cleanRuntimeResourceRoot(value)
	}
	return out
}

func cleanRuntimeResourceRoot(path string) string {
	path = strings.TrimSpace(path)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

func runtimeFailureClass(message string) string {
	if strings.Contains(message, "sandbox_secret_disallowed") {
		return "sandbox_secret_disallowed"
	}
	if strings.Contains(message, "shell_secret_disallowed") {
		return "shell_secret_disallowed"
	}
	if strings.Contains(message, "control manifest digest mismatch") ||
		strings.Contains(message, "expected manifest_") ||
		strings.Contains(message, "expected session_id") ||
		strings.Contains(message, "expected generation_id") ||
		strings.Contains(message, "expected network_profile_id") ||
		strings.Contains(message, "expected agent_runtime_profile_id") ||
		strings.Contains(message, "expected anthropic_api_key_secret_id") ||
		strings.Contains(message, "expected anthropic_auth_token_secret_id") ||
		strings.Contains(message, "expected secret_version") ||
		strings.Contains(message, "secret mount") {
		return "manifest_digest_mismatch"
	}
	if strings.Contains(message, "pre-start sandbox network probe") {
		return "probe_failed_pre_start"
	}
	if strings.Contains(message, "harness-bridge-client probe") ||
		strings.Contains(message, "bridge probe") ||
		strings.Contains(message, "bridge startup probe") ||
		strings.Contains(message, "probe GET /healthz") ||
		strings.Contains(message, "probe POST /v1/messages") {
		return "probe_failed_post_start"
	}
	if strings.Contains(message, "configure sandbox network") {
		return "network_setup_failed"
	}
	return "runtime_failed"
}

func runtimeFailureMessage(errorClass, reason string) string {
	switch errorClass {
	case "probe_failed_pre_start":
		return "sandbox network probe failed before start"
	case "probe_failed_post_start":
		return "sandbox network probe failed after start"
	case "manifest_digest_mismatch":
		return "runtime manifest validation failed"
	case "network_setup_failed":
		return "sandbox network setup failed"
	case "sandbox_secret_disallowed":
		return "sandbox generation cannot mount model secrets"
	case "shell_secret_disallowed":
		return "shell agent cannot mount model secrets"
	default:
		if strings.TrimSpace(reason) != "" {
			return reason
		}
		return "runtime failed"
	}
}

func writeRuntimeStartError(w http.ResponseWriter, err error) {
	if store.IsWorkspaceMutationBusy(err) {
		writeError(w, http.StatusConflict, "workspace mutation in progress")
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	errorClass := runtimeFailureClass(reason)
	writeErrorClass(w, http.StatusInternalServerError, errorClass, runtimeFailureMessage(errorClass, reason))
}

func (s *Server) runtimeGenerationDetails(ctx context.Context, sessionID, generationID string) (store.RuntimeGenerationDetails, error) {
	if strings.TrimSpace(generationID) == "" {
		return store.RuntimeGenerationDetails{}, fmt.Errorf("generation id is required")
	}
	details, err := s.store.GetRuntimeGenerationDetails(ctx, sessionID, generationID)
	if err != nil {
		return store.RuntimeGenerationDetails{}, err
	}
	return details, nil
}

func (s *Server) startColdFallbackSessions(ctx context.Context, owner string) {
	fallbacks, err := s.store.ListColdFallbackSessions(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("cold fallback session list failed", "error", err)
		}
		return
	}
	for _, fallback := range fallbacks {
		ensured, err := s.ensureActiveGeneration(ctx, fallback.Session, owner)
		if err != nil {
			if errors.Is(err, store.ErrPoolExhausted) {
				s.log.Warn("cold fallback pool exhausted", "session_id", fallback.Session.ID, "old_generation_id", fallback.OldGeneration, "queued_turns", fallback.QueuedTurns)
				return
			}
			if !errors.Is(err, context.Canceled) {
				s.log.Warn("cold fallback allocation failed", "session_id", fallback.Session.ID, "old_generation_id", fallback.OldGeneration, "error", err)
			}
			continue
		}
		if !ensured.IsNew {
			continue
		}
		if err := s.startEnsuredGeneration(ctx, fallback.Session, ensured, startFailureInputBlocking); err != nil {
			if !errors.Is(err, context.Canceled) {
				s.log.Warn("cold fallback start failed", "session_id", fallback.Session.ID, "old_generation_id", fallback.OldGeneration, "new_generation_id", ensured.Allocation.GenerationID, "error", err)
			}
			continue
		}
		if err := s.store.UpdateSessionStatusAndActivity(ctx, fallback.Session.ID, string(sessionstate.RunningActive), nil, time.Now().UTC()); err != nil {
			if !errors.Is(err, context.Canceled) {
				s.log.Warn("cold fallback status update failed", "session_id", fallback.Session.ID, "new_generation_id", ensured.Allocation.GenerationID, "error", err)
			}
			continue
		}
		s.hub.Publish(events.Event{Type: "session." + string(sessionstate.RunningActive), SessionID: fallback.Session.ID})
	}
}

func (s *Server) internalProxyRequestStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SandboxSourceIP string `json:"sandbox_source_ip"`
		ProxyRequestID  string `json:"proxy_request_id"`
		UpstreamModel   string `json:"upstream_model"`
		UpstreamBaseURL string `json:"upstream_base_url"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	result, err := s.store.StartProxyRequest(r.Context(), store.StartProxyRequestParams{
		SandboxSourceIP: req.SandboxSourceIP,
		ProxyRequestID:  req.ProxyRequestID,
		UpstreamModel:   req.UpstreamModel,
		UpstreamBaseURL: req.UpstreamBaseURL,
		Now:             time.Now().UTC(),
		TurnLeaseGrace:  s.cfg.Harness.Bridge.ProxyLeaseGrace.Duration,
	})
	if errors.Is(err, store.ErrProxyContextUnavailable) {
		// 503, not 404. This is an internal bookkeeping race — the turn's proxy
		// context was momentarily unresolvable — not a statement that the model or
		// the turn is gone. The distinction decides whether a turn survives: the
		// proxy maps a 4xx to a hard error, and pi's retry classifier only retries
		// on transient-looking failures, so a 404 here ended the turn outright and
		// surfaced to the user as "the model service is unavailable" when nothing
		// was wrong with the model.
		//
		// A retryable status is also the honest description: the condition clears
		// on its own, usually within one heartbeat.
		writeErrorClass(w, http.StatusServiceUnavailable, "active_context_unavailable", "proxy active context unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if result.LeaseWithinGrace {
		// The request was legitimate but its turn lease had already lapsed, so only
		// the grace window admitted it. Worth a warning rather than silence: this is
		// the leading indicator of heartbeats losing the race for the writer
		// connection, which is what made these requests fail outright before.
		s.log.Warn("proxy request admitted on an expired turn lease",
			"session_id", result.SessionID,
			"turn_id", result.TurnID,
			"grace", s.cfg.Harness.Bridge.ProxyLeaseGrace.Duration)
	}
	if !result.Replayed {
		s.publishDurableEvent(r.Context(), result.EventID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":       result.SessionID,
		"turn_id":          result.TurnID,
		"generation_id":    result.GenerationID,
		"request_sequence": result.RequestSequence,
		"event_id":         result.EventID,
		"replayed":         result.Replayed,
	})
}

func (s *Server) internalProxyRequestFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyRequestID             string `json:"proxy_request_id"`
		ProxyConnectLatencyMS      *int64 `json:"proxy_connect_latency_ms"`
		UpstreamFirstByteLatencyMS *int64 `json:"upstream_first_byte_latency_ms"`
		UpstreamTotalLatencyMS     *int64 `json:"upstream_total_latency_ms"`
		RetryCount                 *int64 `json:"retry_count"`
		TimeoutKind                string `json:"timeout_kind"`
		HTTPStatus                 *int64 `json:"http_status"`
		ErrorClass                 string `json:"error_class"`
		Error                      string `json:"error"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	result, err := s.store.FinishProxyRequest(r.Context(), store.FinishProxyRequestParams{
		ProxyRequestID:             req.ProxyRequestID,
		ProxyConnectLatencyMS:      req.ProxyConnectLatencyMS,
		UpstreamFirstByteLatencyMS: req.UpstreamFirstByteLatencyMS,
		UpstreamTotalLatencyMS:     req.UpstreamTotalLatencyMS,
		RetryCount:                 req.RetryCount,
		TimeoutKind:                req.TimeoutKind,
		HTTPStatus:                 req.HTTPStatus,
		ErrorClass:                 req.ErrorClass,
		Error:                      req.Error,
		Now:                        time.Now().UTC(),
	})
	if errors.Is(err, store.ErrProxyRequestUnknown) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "stale_unknown_request"})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !result.Replayed {
		s.publishDurableEvent(r.Context(), result.EventID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "accepted",
		"event_id":      result.EventID,
		"event_type":    result.EventType,
		"session_id":    result.SessionID,
		"turn_id":       result.TurnID,
		"generation_id": result.GenerationID,
		"replayed":      result.Replayed,
	})
}

func (s *Server) requireProxyPeerCredentials(next http.Handler) http.Handler {
	expected := s.cfg.Harness.ProxyServiceIdentity
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, ok := r.Context().Value(proxyPeerCredentialsContextKey{}).(proxyPeerCredentialsResult)
		if !ok {
			writeError(w, http.StatusForbidden, "proxy correlation peer credentials unavailable")
			return
		}
		if result.Err != nil {
			writeError(w, http.StatusForbidden, "proxy correlation peer credentials invalid")
			return
		}
		if result.Credentials.UID != expected.UID || result.Credentials.GID != expected.GID {
			writeError(w, http.StatusForbidden, "proxy correlation peer credentials rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unixPeerCredentials(conn net.Conn) (proxyPeerCredentials, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return proxyPeerCredentials{}, fmt.Errorf("proxy correlation connection is not unix")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return proxyPeerCredentials{}, err
	}
	var credentials *syscall.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return proxyPeerCredentials{}, err
	}
	if controlErr != nil {
		return proxyPeerCredentials{}, controlErr
	}
	if credentials == nil {
		return proxyPeerCredentials{}, fmt.Errorf("proxy correlation peer credentials missing")
	}
	return proxyPeerCredentials{
		UID: int(credentials.Uid),
		GID: int(credentials.Gid),
		PID: int(credentials.Pid),
	}, nil
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
	if session.Agent == string(agents.Shell) {
		if err := s.runtime.Interrupt(sessionID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "interrupting"})
		return
	}

	// Stop is a turn-level operation for pi sessions: the abort travels to the
	// sandbox as a turn-control command, the agent ends the turn, and its process,
	// its session and the workspace all survive. This used to destroy the
	// generation instead — the code asserted there was no portable interrupt
	// primitive — which charged every Stop a full cold start and discarded the
	// workspace state the agent had built up.
	generationID := strings.TrimSpace(session.ActiveGenerationID)
	if generationID == "" {
		writeError(w, http.StatusConflict, "session has no active generation")
		return
	}
	generationStatus, err := s.store.GetRuntimeGenerationStatus(r.Context(), sessionID, generationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Which turn to abort has to be read before the interrupt: the transaction
	// retires it, so afterwards there is nothing left to name. The command carries
	// the turn id so a slow delivery cannot abort whatever turn came next.
	snapshot, err := s.store.GetSessionRuntimeSnapshot(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A failed or destroyed generation has no sandbox to deliver to, and no turn is
	// really executing inside one. Skipping delivery keeps Stop working as the
	// escape hatch for a stale running_active left by a crash.
	sandboxLive := generationStatus != "failed" && generationStatus != "destroyed"
	if sandboxLive && snapshot.ActiveTurn != nil {
		details, detailsErr := s.runtimeGenerationDetails(r.Context(), sessionID, generationID)
		if detailsErr != nil {
			writeError(w, http.StatusInternalServerError, detailsErr.Error())
			return
		}
		// Deliver before committing, as InterruptSession documents. The reverse
		// order would mark the turn canceled while the agent kept working, and the
		// completion ack for an already-retired turn is discarded — so the user
		// would watch a "stopped" turn keep streaming.
		if _, err := bridge.AppendTurnControl(details.BridgeDirPath, bridge.TurnControlCommand{
			TurnID:  snapshot.ActiveTurn.ID,
			Command: bridge.TurnControlAbort,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("deliver interrupt to sandbox: %v", err))
			return
		}
	}
	result, err := s.store.InterruptSession(r.Context(), store.InterruptSessionParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		OwnerUUID:    s.ownerUUID,
		ErrorClass:   "user_interrupted",
		Reason:       "interrupted by user",
		Now:          time.Now().UTC(),
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	for _, eventID := range result.EventIDs {
		s.publishDurableEvent(r.Context(), eventID)
	}
	s.publishSessionRuntime(sessionID, result.Runtime)
	// pending_queued counts the turns Stop parked. They survive and stay visible,
	// but nothing dispatches them until the user releases each one — a queued
	// prompt behind a Stop is usually stale, and running it a moment later is the
	// same surprise the user pressed Stop to prevent.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":         result.SessionStatus,
		"pending_queued": result.PendingQueued,
		"runtime":        result.Runtime,
	})
}

func (s *Server) destroySession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, ok := s.authorizeSession(w, r, sessionID)
	if !ok {
		return
	}
	if session.ActiveGenerationID != "" && session.Status != string(sessionstate.Checkpointed) {
		details, err := s.runtimeGenerationDetails(r.Context(), session.ID, session.ActiveGenerationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.destroyGenerationRuntime(r.Context(), details); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	now := time.Now().UTC()
	result, err := s.store.DestroySession(r.Context(), sessionID, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, generationID := range result.GenerationIDs {
		if err := s.cleanupGenerationResources(r.Context(), sessionID, generationID, now); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	status := string(sessionstate.Destroyed)
	s.publishDurableEvent(r.Context(), result.EventID)
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// listArtifacts lists the whole verified workspace. An identity share exposes
// that full set by design (see the proposal's trace-vs-artifact section); the
// per-file whitelist exists only for anonymous links.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request, sessionID string) {
	if _, _, _, ok := s.authorizeRead(w, r, sessionID); !ok {
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

	volumeConfig, err := s.dataVolumeProvisionerConfig()
	if err != nil {
		return nil, workspace.File{}, http.StatusInternalServerError, err.Error()
	}
	volume, err := (localworkspace.Volumes{Records: s.store}).VerifySessionWorkspaceVolume(ctx, localworkspace.VerifySessionWorkspaceVolumeParams{
		SessionID: sessionID,
		Config:    volumeConfig,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, workspace.File{}, http.StatusNotFound, "artifact not found"
	}
	if err != nil {
		return nil, workspace.File{}, http.StatusForbidden, "workspace evidence invalid"
	}

	file, info, err := workspace.Open(ctx, localworkspace.Directory{Path: volume.HostPath, UID: volume.SandboxUID, GID: volume.SandboxGID}, cleanPath)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return nil, workspace.File{}, http.StatusNotFound, "artifact not found"
		case errors.Is(err, workspace.ErrForbidden), errors.Is(err, os.ErrPermission):
			return nil, workspace.File{}, http.StatusForbidden, "artifact path is not a regular file or directory"
		default:
			return nil, workspace.File{}, http.StatusInternalServerError, err.Error()
		}
	}
	return file, info, 0, ""
}

func (s *Server) dataVolumeProvisionerConfig() (localworkspace.DataVolumeProvisionerConfig, error) {
	roots, err := config.ValidateIsolationRoots(s.cfg.IsolationRoots())
	if err != nil {
		return localworkspace.DataVolumeProvisionerConfig{}, err
	}
	identity := s.cfg.Harness.SandboxIdentity
	return localworkspace.DataVolumeProvisionerConfig{
		SessionsRoot:   roots.SessionsRoot,
		AgentHomesRoot: roots.AgentHomesRoot,
		EvidenceRoot:   roots.DataVolumeEvidenceRoot,
		LayoutVersion:  localworkspace.DataVolumeLayoutVersion,
		RuntimeIdentity: store.RuntimeIdentity{
			UID:              identity.UID,
			GID:              identity.GID,
			SupplementalGIDs: identity.SupplementalGIDs,
		},
	}, nil
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

func (s *Server) MonitorIdleSessions(ctx context.Context) error {
	if strings.EqualFold(strings.TrimSpace(s.cfg.RunscNetwork), "host") {
		s.log.Info("idle checkpoint monitor disabled because runsc host network is not checkpointable")
		return nil
	}
	if !s.cfg.Harness.Checkpoint.AutoEnabled {
		s.log.Info("idle checkpoint monitor disabled by policy")
		return nil
	}
	if strings.TrimSpace(s.ownerUUID) == "" {
		return fmt.Errorf("idle checkpoint monitor requires owner uuid")
	}

	owner := store.GenerationLeaseOwner(s.ownerUUID)
	interval := s.cfg.Harness.Checkpoint.MonitorInterval.Duration
	if interval <= 0 {
		interval = idleCheckpointInterval
	}
	idleThreshold := s.cfg.Harness.Checkpoint.IdleThreshold.Duration
	if idleThreshold < 0 {
		idleThreshold = idleCheckpointThreshold
	}
	tick := func(now time.Time) {
		candidates, err := s.store.ListIdleReleaseCandidates(ctx, owner, now, idleThreshold)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.log.Warn("failed to list idle release candidates", "error", err)
			}
			return
		}
		for _, candidate := range candidates {
			if err := s.releaseIdleGeneration(ctx, candidate, owner, now); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("idle sandbox release failed", "session_id", candidate.SessionID, "generation_id", candidate.GenerationID, "error", err)
			}
		}
	}

	tick(time.Now().UTC())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			tick(now.UTC())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// releaseIdleGeneration tears down an idle session's sandbox to free compute,
// without taking a runsc checkpoint. The session moves to 'checkpointed' (its
// logical "released, wake me later" state with no hot-restore image) and the
// generation's compute/network slots are reclaimed and destroyed. Waking the
// session (next message) allocates a fresh generation and cold-starts against
// the retained workspace/agent-home.
func (s *Server) releaseIdleGeneration(ctx context.Context, candidate store.CheckpointCandidate, owner string, now time.Time) error {
	if err := s.store.ReleaseIdleGeneration(ctx, candidate.SessionID, candidate.GenerationID, owner, now); err != nil {
		return err
	}
	// The generation is now failed with reclaimable resources; tear down the
	// sandbox and mark resources destroyed via the shared cleanup path.
	if err := s.cleanupGenerationResources(ctx, candidate.SessionID, candidate.GenerationID, time.Now().UTC()); err != nil {
		// The DB transition already succeeded and resources are reclaimable, so
		// the reaper will finish teardown; surface the error but do not roll back.
		s.log.Warn("idle release sandbox teardown deferred to reaper", "session_id", candidate.SessionID, "generation_id", candidate.GenerationID, "error", err)
	}
	s.hub.Publish(events.Event{Type: "session." + string(sessionstate.Checkpointed), SessionID: candidate.SessionID, GenerationID: candidate.GenerationID})
	return nil
}

// internalDataProxyAuthorize answers a data-plane gateway's per-connection and
// per-query authorization question.
//
// It is a pure decision: no events, no counters, nothing written. In particular it
// does not touch next_request_sequence, which belongs to the model proxy — see the
// note on store.AuthorizeDataProxyRequest.
//
// A denial is 403 with a short, agent-readable reason, because the gateway puts
// that reason into an ERR packet the agent actually reads. Anything unexpected is
// an error rather than a grant: the gateway must fail closed.
func (s *Server) internalDataProxyAuthorize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain          string `json:"domain"`
		SandboxSourceIP string `json:"sandbox_source_ip"`
	}
	if !decodeJSONBody(w, r, maxControlBodyBytes, &req) {
		return
	}
	if strings.TrimSpace(req.SandboxSourceIP) == "" {
		writeError(w, http.StatusBadRequest, "sandbox_source_ip is required")
		return
	}

	auth, err := s.store.AuthorizeDataProxyRequest(r.Context(), store.AuthorizeDataProxyRequestParams{
		Domain:          store.DataProxyDomain(req.Domain),
		SandboxSourceIP: req.SandboxSourceIP,
		Now:             time.Now().UTC(),
	})
	if errors.Is(err, store.ErrDataProxyContextUnavailable) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"authorized": false,
			"reason":     s.store.DataProxyDenialReason(r.Context(), req.SandboxSourceIP),
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorized":    true,
		"session_id":    auth.SessionID,
		"generation_id": auth.GenerationID,
		"turn_id":       auth.TurnID,
	})
}
