package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/store"
)

func (f *shareFixture) mintLink(t *testing.T, body string) (string, apiShareLink) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_alice/share-links", strings.NewReader(body)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint link: want 201 got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Link  apiShareLink `json:"link"`
		Token string       `json:"token"`
		Path  string       `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode mint: %v", err)
	}
	if payload.Token == "" {
		t.Fatal("expected a raw token in the create response")
	}
	if payload.Path != "/s/"+payload.Token {
		t.Fatalf("unexpected share path %q", payload.Path)
	}
	return payload.Token, payload.Link
}

func sharedGet(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.sharedAPI(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The spec's routing requirement: /api/shared/* must NOT pass through
// requireAuth, while /api/* must. Both directions are pinned because a future
// route edit could break either one silently -- one way every anonymous request
// 401s, the other way the anonymous handlers run inside the authed tree.
func TestSharedSubtreeBypassesAuthWhileAPIRequiresIt(t *testing.T) {
	f := newShareFixture(t)
	routes := f.srv.Routes()

	// No cookie: the authed tree rejects.
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/sessions without a cookie: want 401 got %d (%s)", rec.Code, rec.Body.String())
	}

	// The shared subtree is reached without one. An unknown token is a 404 from
	// the handler, which proves the request got past the auth boundary rather
	// than being turned away at it.
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/shared/not-a-real-token", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/api/shared/{bogus} without a cookie: want 404 got %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "login required") {
		t.Fatal("the shared subtree fell through to requireAuth")
	}

	// And a real token resolves anonymously end to end.
	token, _ := f.mintLink(t, `{}`)
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/shared/"+token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/shared/{token}: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("the token is in the URL path, so Referrer-Policy must be no-referrer, got %q", got)
	}
}

// Writes must not exist on the anonymous subtree at all.
func TestSharedSubtreeIsGetOnly(t *testing.T) {
	f := newShareFixture(t)
	token, _ := f.mintLink(t, `{}`)
	routes := f.srv.Routes()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(method, "/api/shared/"+token, strings.NewReader(`{}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s on the shared subtree: want 405 got %d", method, rec.Code)
		}
	}
}

// A public link must not carry the owner's identity or internal failure detail,
// and must not expose the raw status enum.
func TestSharedSessionDTOIsMinimal(t *testing.T) {
	f := newShareFixture(t)
	if err := f.store.FailSession(context.Background(), store.FailSessionParams{
		SessionID: "sess_alice", ErrorClass: "runtime_failed", Reason: "/var/lib/harness/sessions/sess_alice blew up",
	}); err != nil {
		t.Fatalf("fail session: %v", err)
	}
	token, _ := f.mintLink(t, `{}`)

	rec := sharedGet(t, f.srv, "/api/shared/"+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, forbidden := range []string{"user_id", "failure_reason", "error_class", "workspace", "agent", "restore_id"} {
		if _, present := payload[forbidden]; present {
			t.Fatalf("public session DTO must not carry %s: %v", forbidden, payload)
		}
	}
	if payload["status"] != "ended" {
		t.Fatalf("expected the coarsened status \"ended\", got %v", payload["status"])
	}
	if payload["id"] != "sess_alice" {
		t.Fatalf("expected the session id, got %v", payload["id"])
	}
}

// A link is a snapshot: turns that happen after it was created must not appear.
func TestSharedMessagesRespectMaxMessageID(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	if _, err := f.store.AddMessage(ctx, "sess_alice", "user", "first question"); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if _, err := f.store.AddAssistantMessage(ctx, "sess_alice", "assistant", "first answer", "stop"); err != nil {
		t.Fatalf("add answer: %v", err)
	}

	token, link := f.mintLink(t, `{"visibility":"full"}`)
	if link.MaxMessageID == nil {
		t.Fatal("expected the link to freeze a max_message_id")
	}

	// The conversation continues after the link was handed out.
	if _, err := f.store.AddMessage(ctx, "sess_alice", "user", "later secret question"); err != nil {
		t.Fatalf("add later message: %v", err)
	}
	if _, err := f.store.AddAssistantMessage(ctx, "sess_alice", "assistant", "later secret answer", "stop"); err != nil {
		t.Fatalf("add later answer: %v", err)
	}

	rec := sharedGet(t, f.srv, "/api/shared/"+token+"/messages")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "first answer") {
		t.Fatalf("expected the frozen conversation, got %s", body)
	}
	if strings.Contains(body, "later secret") {
		t.Fatalf("a link must not publish turns added after it was created: %s", body)
	}
	// The public message DTO carries no session_id or feedback.
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, message := range payload.Messages {
		for _, forbidden := range []string{"session_id", "feedback"} {
			if _, present := message[forbidden]; present {
				t.Fatalf("public message DTO must not carry %s: %v", forbidden, message)
			}
		}
	}
}

// summary is the default for a public link, and must strip traces.
func TestSharedLinkDefaultsToSummaryAndStripsTrace(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	answer, err := f.store.AddAssistantMessage(ctx, "sess_alice", "assistant", "the answer", "stop")
	if err != nil {
		t.Fatalf("add message: %v", err)
	}
	if err := f.store.SaveMessageTrace(ctx, answer.ID, "sess_alice",
		[]byte(`[{"kind":"tool","command":"mysql -h doris-fe.internal -e 'select * from cells'"}]`)); err != nil {
		t.Fatalf("save trace: %v", err)
	}

	token, link := f.mintLink(t, `{}`)
	if link.Visibility != store.ShareVisibilitySummary {
		t.Fatalf("a public link must default to summary, got %q", link.Visibility)
	}
	rec := sharedGet(t, f.srv, "/api/shared/"+token+"/messages")
	body := rec.Body.String()
	if strings.Contains(body, "doris-fe.internal") || strings.Contains(body, "trace") {
		t.Fatalf("summary must strip the execution trace: %s", body)
	}
	if !strings.Contains(body, "the answer") {
		t.Fatalf("expected the reply text: %s", body)
	}

	// full opts in explicitly.
	fullToken, _ := f.mintLink(t, `{"visibility":"full"}`)
	rec = sharedGet(t, f.srv, "/api/shared/"+fullToken+"/messages")
	if !strings.Contains(rec.Body.String(), "doris-fe.internal") {
		t.Fatalf("full visibility must include the trace: %s", rec.Body.String())
	}
}

// A shell session's "summary" would be an empty page, so it is rejected rather
// than silently satisfied.
func TestShareLinkForcesFullOnShellSessions(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	shell := store.Session{
		ID: "sess_shell", UserID: "alice", Status: "created", Agent: "sh", Mode: "shell",
		Workspace: f.dir, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateSession(ctx, shell); err != nil {
		t.Fatalf("create shell session: %v", err)
	}

	// An explicit summary request is a 400.
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_shell/share-links",
		strings.NewReader(`{"visibility":"summary"}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_shell")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("shell + summary: want 400 got %d (%s)", rec.Code, rec.Body.String())
	}

	// Omitting visibility yields full rather than the usual summary default.
	rec = httptest.NewRecorder()
	req = asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_shell/share-links",
		strings.NewReader(`{}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_shell")
	if rec.Code != http.StatusCreated {
		t.Fatalf("shell default: want 201 got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Link apiShareLink `json:"link"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Link.Visibility != store.ShareVisibilityFull {
		t.Fatalf("a shell link must be full, got %q", payload.Link.Visibility)
	}
}

func TestShareLinkTTLIsCappedAndMandatory(t *testing.T) {
	f := newShareFixture(t)
	f.srv.cfg.MaxShareLinkTTL = 48 * time.Hour

	// Over the cap: rejected.
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_alice/share-links",
		strings.NewReader(`{"ttl_seconds":864000}`)), "alice")
	f.srv.createShareLink(rec, req, "sess_alice")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ttl over the cap: want 400 got %d (%s)", rec.Code, rec.Body.String())
	}

	// Omitted: falls back to the cap, never to "never expires".
	_, link := f.mintLink(t, `{}`)
	if link.ExpiresAt.IsZero() {
		t.Fatal("a public link must always have an expiry")
	}
	if remaining := time.Until(link.ExpiresAt); remaining > 48*time.Hour+time.Minute {
		t.Fatalf("expiry exceeds the cap: %s", remaining)
	}
}

// Revoke and rotate must take effect immediately, not after the token cache TTL.
func TestShareLinkRevokeAndRotateInvalidateImmediately(t *testing.T) {
	f := newShareFixture(t)
	token, link := f.mintLink(t, `{}`)

	// Prime the cache with a successful anonymous read.
	if rec := sharedGet(t, f.srv, "/api/shared/"+token); rec.Code != http.StatusOK {
		t.Fatalf("initial read: want 200 got %d", rec.Code)
	}

	// Rotate: the old URL stops working, the new one works.
	rec := httptest.NewRecorder()
	f.srv.rotateShareLink(rec, asUser(httptest.NewRequest(http.MethodPost, "/rotate", nil), "alice"), "sess_alice", link.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	var rotated struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
		t.Fatalf("decode rotate: %v", err)
	}
	if got := sharedGet(t, f.srv, "/api/shared/"+token); got.Code != http.StatusNotFound {
		t.Fatalf("rotated-away token must 404 immediately, got %d", got.Code)
	}
	if got := sharedGet(t, f.srv, "/api/shared/"+rotated.Token); got.Code != http.StatusOK {
		t.Fatalf("new token: want 200 got %d (%s)", got.Code, got.Body.String())
	}

	// Revoke: the new URL stops working too.
	rec = httptest.NewRecorder()
	f.srv.deleteShareLink(rec, asUser(httptest.NewRequest(http.MethodDelete, "/link", nil), "alice"), "sess_alice", link.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := sharedGet(t, f.srv, "/api/shared/"+rotated.Token); got.Code != http.StatusNotFound {
		t.Fatalf("revoked token must 404 immediately, got %d", got.Code)
	}
}

// Managing links is an owner power; a reader must not be able to mint one.
func TestShareLinkManagementIsOwnerOnly(t *testing.T) {
	f := newShareFixture(t)
	f.grant(t, "bob", store.ShareVisibilityFull)
	token, link := f.mintLink(t, `{}`)

	handlers := map[string]func(w http.ResponseWriter, r *http.Request){
		"create": func(w http.ResponseWriter, r *http.Request) { f.srv.createShareLink(w, r, "sess_alice") },
		"list":   func(w http.ResponseWriter, r *http.Request) { f.srv.listShareLinks(w, r, "sess_alice") },
		"rotate": func(w http.ResponseWriter, r *http.Request) { f.srv.rotateShareLink(w, r, "sess_alice", link.ID) },
		"revoke": func(w http.ResponseWriter, r *http.Request) { f.srv.deleteShareLink(w, r, "sess_alice", link.ID) },
	}
	for name, handler := range handlers {
		rec := httptest.NewRecorder()
		handler(rec, asUser(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`)), "bob"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s as a reader: want 404 got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}
	// The reader's attempts changed nothing.
	if got := sharedGet(t, f.srv, "/api/shared/"+token); got.Code != http.StatusOK {
		t.Fatalf("the owner's link should still work, got %d", got.Code)
	}
}

// The owner-facing listing must never echo a token back.
func TestListShareLinksNeverReturnsToken(t *testing.T) {
	f := newShareFixture(t)
	token, _ := f.mintLink(t, `{}`)

	rec := httptest.NewRecorder()
	f.srv.listShareLinks(rec, asUser(httptest.NewRequest(http.MethodGet, "/links", nil), "alice"), "sess_alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, token) || strings.Contains(body, "token") {
		t.Fatalf("the link listing must not echo the token: %s", body)
	}
}

// Access counters are buffered in memory and only reach the database through the
// maintenance flush, so an anonymous read never takes the writer's connection.
func TestShareLinkAccessCountsFlushOffTheRequestPath(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	token, link := f.mintLink(t, `{}`)

	for i := 0; i < 3; i++ {
		if rec := sharedGet(t, f.srv, "/api/shared/"+token); rec.Code != http.StatusOK {
			t.Fatalf("read %d: want 200 got %d", i, rec.Code)
		}
	}

	// Nothing persisted yet.
	links, err := f.store.ListShareLinks(ctx, "sess_alice")
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 1 || links[0].AccessCount != 0 {
		t.Fatalf("counts must not be written on the request path: %+v", links)
	}

	if err := f.srv.flushShareLinkAccess(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	links, err = f.store.ListShareLinks(ctx, "sess_alice")
	if err != nil {
		t.Fatalf("list links after flush: %v", err)
	}
	if len(links) != 1 || links[0].ID != link.ID || links[0].AccessCount != 3 {
		t.Fatalf("expected 3 buffered views flushed, got %+v", links)
	}
	if links[0].LastAccessedAt == nil {
		t.Fatal("expected last_accessed_at to be set by the flush")
	}

	// A second flush with nothing buffered is a no-op, not a repeated increment.
	if err := f.srv.flushShareLinkAccess(ctx); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	links, _ = f.store.ListShareLinks(ctx, "sess_alice")
	if links[0].AccessCount != 3 {
		t.Fatalf("expected the count to stay at 3, got %d", links[0].AccessCount)
	}
}

// Phase 2 mints text-only links: the whitelist is empty, and the anonymous
// artifact listing reflects that rather than falling back to the live workspace.
func TestSharedArtifactsReturnsWhitelistNotWorkspace(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	if err := f.store.UpsertArtifact(ctx, store.Artifact{
		SessionID: "sess_alice", Path: "intermediate-dump.csv", Size: 4096, ModTime: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	token, _ := f.mintLink(t, `{}`)

	rec := sharedGet(t, f.srv, "/api/shared/"+token+"/artifacts")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "intermediate-dump.csv") {
		t.Fatalf("a public link must expose its whitelist, never the live workspace: %s", rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"artifacts":[]}` {
		t.Fatalf("expected an empty whitelist, got %s", got)
	}
}

func TestSharedLinkUnknownPathIs404(t *testing.T) {
	f := newShareFixture(t)
	token, _ := f.mintLink(t, `{}`)
	for _, path := range []string{
		"/api/shared/",
		"/api/shared/" + token + "/nope",
		"/api/shared/" + token + "/messages/extra",
	} {
		if rec := sharedGet(t, f.srv, path); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404 got %d", path, rec.Code)
		}
	}
}
