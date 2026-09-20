package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func newAuthTestServer(t *testing.T, dir string, st *store.Store, users []config.AuthUser) *Server {
	t.Helper()
	cfg := config.Config{

		MaxSessions:     10,
		DefaultAgent:    "pi",
		CookieName:      "harness_auth",
		AuthUsers:       users,
		AuthSessionTTL:  time.Hour,
		MaxShareLinkTTL: 30 * 24 * time.Hour,
	}
	return &Server{workspaceInventory: fixtureInventory{}, retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		cfg:   cfg,
		store: st,

		hub:               events.NewHub(),
		log:               slog.Default(),
		creds:             newAuthRegistry(cfg, slog.Default()),
		loginLimiter:      auth.NewRateLimiter(5, 15*time.Minute),
		sessionOwnerCache: map[string]sessionOwnerEntry{},
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := auth.HashPasswordWithIterations(pw, 1000)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}

func TestLoginSuccessAndCookieAuth(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newAuthTestServer(t, dir, st, []config.AuthUser{{Username: "lab", PasswordHash: mustHash(t, "s3cret")}})

	// Wrong password -> 401.
	rec := httptest.NewRecorder()
	srv.login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"lab","password":"nope"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: want 401 got %d", rec.Code)
	}

	// Correct password -> 200 + cookie.
	rec = httptest.NewRecorder()
	srv.login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"lab","password":"s3cret"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected a session cookie")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly {
		t.Fatalf("cookie must be HttpOnly")
	}

	// requireAuth accepts the cookie and injects the user id.
	var gotUser string
	handler := srv.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = userIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	authReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	authReq.AddCookie(cookie)
	authRec := httptest.NewRecorder()
	handler.ServeHTTP(authRec, authReq)
	if authRec.Code != http.StatusOK || gotUser != "lab" {
		t.Fatalf("requireAuth: code=%d user=%q", authRec.Code, gotUser)
	}

	// No cookie -> 401.
	noRec := httptest.NewRecorder()
	handler.ServeHTTP(noRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if noRec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: want 401 got %d", noRec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newAuthTestServer(t, dir, st, []config.AuthUser{{Username: "lab", PasswordHash: mustHash(t, "s3cret")}})
	srv.loginLimiter = auth.NewRateLimiter(3, 15*time.Minute)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		srv.login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"lab","password":"bad"}`)))
	}
	// Next attempt should be rate-limited even with the correct password.
	rec := httptest.NewRecorder()
	srv.login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"lab","password":"s3cret"}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 after repeated failures, got %d", rec.Code)
	}
}

func TestSessionIsolationAcrossUsers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newAuthTestServer(t, dir, st, []config.AuthUser{
		{Username: "alice", PasswordHash: mustHash(t, "pw")},
		{Username: "bob", PasswordHash: mustHash(t, "pw")},
	})

	now := time.Now().UTC()
	aliceSession := store.Session{
		ID: "sess_alice", UserID: "alice", Status: string(sessionstate.Created),
		Agent: "pi", Mode: "agent", Workspace: dir,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, aliceSession); err != nil {
		t.Fatalf("create alice session: %v", err)
	}

	withUser := func(r *http.Request, user string) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), userContextKey{}, user))
	}

	// Alice sees her session.
	rec := httptest.NewRecorder()
	srv.getSession(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice", nil), "alice"), "sess_alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("alice should see her session, got %d", rec.Code)
	}

	// Bob gets a 404 for Alice's session.
	rec = httptest.NewRecorder()
	srv.getSession(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice", nil), "bob"), "sess_alice")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob must not see alice session, got %d", rec.Code)
	}

	// listSessions is scoped per user.
	rec = httptest.NewRecorder()
	srv.listSessions(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/sessions", nil), "bob"))
	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Sessions) != 0 {
		t.Fatalf("bob should see no sessions, got %d", len(payload.Sessions))
	}
}

func TestMeReportsAuthState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newAuthTestServer(t, dir, st, []config.AuthUser{{Username: "lab", PasswordHash: mustHash(t, "pw")}})

	rec := httptest.NewRecorder()
	srv.me(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["auth_required"] != true {
		t.Fatalf("want auth_required true, got %v", body["auth_required"])
	}
	if body["user_id"] != "" {
		t.Fatalf("want empty user_id when not logged in, got %v", body["user_id"])
	}
}

func TestAdminFeatureFlagRequiresRoleAndAudits(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := newAuthTestServer(t, dir, st, []config.AuthUser{{Username: "admin", PasswordHash: mustHash(t, "pw"), Role: "admin"}, {Username: "alice", PasswordHash: mustHash(t, "pw")}})
	withCookie := func(user string) *http.Request {
		loginRec := httptest.NewRecorder()
		srv.issueAuthSession(loginRec, httptest.NewRequest(http.MethodPost, "/api/login", nil), user)
		cookie := loginRec.Result().Cookies()[0]
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/feature-flags/cron_agent", strings.NewReader(`{"enabled":true,"reason":"enable MVP"}`))
		req.AddCookie(cookie)
		return req
	}
	rec := httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.adminAPI)).ServeHTTP(rec, withCookie("alice"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("regular user status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.requireAdmin(http.HandlerFunc(srv.adminAPI)).ServeHTTP(rec, withCookie("admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin update status=%d body=%s", rec.Code, rec.Body.String())
	}
	entries, err := st.ListAudit(ctx, 10, 0)
	if err != nil || len(entries) != 1 || entries[0].Action != "feature_flag.update" {
		t.Fatalf("audit=%+v err=%v", entries, err)
	}
}

func TestOperationalDiagnosticsRequiresAdministrator(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := newAuthTestServer(t, dir, st, []config.AuthUser{{Username: "admin", PasswordHash: mustHash(t, "pw"), Role: "admin"}, {Username: "alice", PasswordHash: mustHash(t, "pw")}})
	srv.SetDiagnostics(func(context.Context) (any, error) { return map[string]int{"pending": 1}, nil })
	for user, want := range map[string]int{"alice": http.StatusForbidden, "admin": http.StatusOK} {
		login := httptest.NewRecorder()
		srv.issueAuthSession(login, httptest.NewRequest(http.MethodPost, "/api/login", nil), user)
		request := httptest.NewRequest(http.MethodGet, "/api/admin/diagnostics", nil)
		request.AddCookie(login.Result().Cookies()[0])
		response := httptest.NewRecorder()
		srv.Routes().ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s diagnostics: %d", user, response.Code)
		}
	}
}
