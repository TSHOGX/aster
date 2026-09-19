package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
	localworkspace "harness-platform/orchestrator/internal/workspace/local"
)

// shareFixture is a two-user server with one session owned by alice.
type shareFixture struct {
	srv     *Server
	store   *store.Store
	session store.Session
	dir     string
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newAuthTestServer(t, dir, st, []config.AuthUser{
		{Username: "alice", PasswordHash: mustHash(t, "pw")},
		{Username: "bob", PasswordHash: mustHash(t, "pw")},
		{Username: "carol", PasswordHash: mustHash(t, "pw")},
	})

	now := time.Now().UTC()
	session := store.Session{
		ID: "sess_alice", UserID: "alice", Status: string(sessionstate.Created),
		Agent: "pi", Mode: "agent", Workspace: dir, RestoreID: "r1",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &shareFixture{srv: srv, store: st, session: session, dir: dir}
}

func asUser(r *http.Request, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userContextKey{}, user))
}

func (f *shareFixture) grant(t *testing.T, grantee, visibility string) string {
	t.Helper()
	body := `{"grantee":"` + grantee + `","visibility":"` + visibility + `"}`
	rec := httptest.NewRecorder()
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_alice/shares", strings.NewReader(body)), "alice")
	f.srv.createSessionShare(rec, req, "sess_alice")
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant to %s: want 201 got %d (%s)", grantee, rec.Code, rec.Body.String())
	}
	var share apiSessionShare
	if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
		t.Fatalf("decode share: %v", err)
	}
	return share.ID
}

// The three properties the proposal names: no share reads 404, a share reads
// 200, and a share must NOT unlock any write route.
func TestReadGateGrantsReadsButNeverWrites(t *testing.T) {
	f := newShareFixture(t)

	// Before any grant, bob cannot read.
	rec := httptest.NewRecorder()
	f.srv.getSession(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice", nil), "bob"), "sess_alice")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ungranted read: want 404 got %d", rec.Code)
	}

	f.grant(t, "bob", store.ShareVisibilityFull)

	// Read routes now succeed for bob.
	reads := map[string]func(w http.ResponseWriter, r *http.Request){
		"getSession":    func(w http.ResponseWriter, r *http.Request) { f.srv.getSession(w, r, "sess_alice") },
		"listMessages":  func(w http.ResponseWriter, r *http.Request) { f.srv.listMessages(w, r, "sess_alice") },
		"listArtifacts": func(w http.ResponseWriter, r *http.Request) { f.srv.listArtifacts(w, r, "sess_alice") },
	}
	for name, handler := range reads {
		rec := httptest.NewRecorder()
		handler(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice", nil), "bob"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with a grant: want 200 got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}

	// Write routes stay owner-only. Each must 404 for a reader, not 403 or 409:
	// a reader is told nothing about the session's state.
	writes := map[string]func(w http.ResponseWriter, r *http.Request){
		"renameSession": func(w http.ResponseWriter, r *http.Request) { f.srv.renameSession(w, r, "sess_alice") },
		"destroySession": func(w http.ResponseWriter, r *http.Request) {
			f.srv.destroySession(w, r, "sess_alice")
		},
		"sendMessage": func(w http.ResponseWriter, r *http.Request) { f.srv.sendMessage(w, r, "sess_alice") },
		"interruptSession": func(w http.ResponseWriter, r *http.Request) {
			f.srv.interruptSession(w, r, "sess_alice")
		},
		"uploadArtifacts": func(w http.ResponseWriter, r *http.Request) { f.srv.uploadArtifacts(w, r, "sess_alice") },
		"createShare": func(w http.ResponseWriter, r *http.Request) {
			f.srv.createSessionShare(w, r, "sess_alice")
		},
		"listShares":  func(w http.ResponseWriter, r *http.Request) { f.srv.listSessionShares(w, r, "sess_alice") },
		"deleteShare": func(w http.ResponseWriter, r *http.Request) { f.srv.deleteSessionShare(w, r, "sess_alice", "share_x") },
	}
	for name, handler := range writes {
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"title":"hijacked","content":"hijacked","grantee":"carol"}`)
		req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_alice", body), "bob")
		handler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s as reader: want 404 got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}

	// The write attempt must not have changed anything.
	session, err := f.store.GetSession(context.Background(), "sess_alice")
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if session.Title != "" || session.Status != string(sessionstate.Created) {
		t.Fatalf("reader mutated the session: %+v", session)
	}
}

// A reader must not learn the owner's username or the session's failure detail.
func TestSharedSessionDTOStripsOwnerAndFailureFields(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	if err := f.store.FailSession(ctx, store.FailSessionParams{
		SessionID:  "sess_alice",
		ErrorClass: "runtime_failed",
		Reason:     "sandbox exploded at /var/lib/harness/sessions/sess_alice",
	}); err != nil {
		t.Fatalf("fail session: %v", err)
	}
	f.grant(t, "bob", store.ShareVisibilityFull)

	rec := httptest.NewRecorder()
	f.srv.getSession(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice", nil), "bob"), "sess_alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("reader get: want 200 got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"user_id", "failure_reason", "error_class"} {
		if value, present := payload[field]; present {
			t.Fatalf("reader payload must not carry %s, got %v", field, value)
		}
	}
	if payload["id"] != "sess_alice" {
		t.Fatalf("expected the session id to survive trimming, got %v", payload["id"])
	}

	// The owner still sees all three.
	rec = httptest.NewRecorder()
	f.srv.getSession(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice", nil), "alice"), "sess_alice")
	var ownerPayload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ownerPayload); err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	if ownerPayload["user_id"] != "alice" || ownerPayload["error_class"] != "runtime_failed" {
		t.Fatalf("owner payload was trimmed: %v", ownerPayload)
	}
}

// summary must strip traces and intermediate tool steps, but a shell session's
// output has an EMPTY stop_reason and must survive. Filtering on
// stop_reason == "stop" would delete it.
func TestSummaryVisibilityKeepsShellOutputAndDropsTraces(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()

	if _, err := f.store.AddMessage(ctx, "sess_alice", "user", "ls -la"); err != nil {
		t.Fatalf("add user message: %v", err)
	}
	// Shell output: AddMessage leaves stop_reason empty.
	shellOut, err := f.store.AddMessage(ctx, "sess_alice", "assistant", "total 12\ndrwxr-xr-x")
	if err != nil {
		t.Fatalf("add shell output: %v", err)
	}
	if err := f.store.SaveMessageTrace(ctx, shellOut.ID, "sess_alice",
		[]byte(`[{"kind":"tool","command":"ls -la"}]`)); err != nil {
		t.Fatalf("save trace: %v", err)
	}
	// An intermediate tool-use step, which summary must drop.
	if _, err := f.store.AddAssistantMessage(ctx, "sess_alice", "assistant", "calling a tool", "toolUse"); err != nil {
		t.Fatalf("add tool step: %v", err)
	}
	// A turn-final reply.
	if _, err := f.store.AddAssistantMessage(ctx, "sess_alice", "assistant", "here is the answer", "stop"); err != nil {
		t.Fatalf("add final: %v", err)
	}

	shareID := f.grant(t, "bob", store.ShareVisibilitySummary)
	if shareID == "" {
		t.Fatal("expected a share id")
	}

	rec := httptest.NewRecorder()
	f.srv.listMessages(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice/messages", nil), "bob"), "sess_alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("reader messages: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Messages []store.Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var contents []string
	for _, message := range payload.Messages {
		contents = append(contents, message.Content)
		if message.Trace != nil {
			t.Fatalf("summary must strip traces, message %d kept one", message.ID)
		}
	}
	joined := strings.Join(contents, "|")
	if !strings.Contains(joined, "total 12") {
		t.Fatalf("summary dropped shell output (the empty-stop_reason case): %q", joined)
	}
	if !strings.Contains(joined, "here is the answer") {
		t.Fatalf("summary dropped the turn-final reply: %q", joined)
	}
	if strings.Contains(joined, "calling a tool") {
		t.Fatalf("summary kept an intermediate toolUse step: %q", joined)
	}

	// full keeps the trace.
	f.grant(t, "carol", store.ShareVisibilityFull)
	rec = httptest.NewRecorder()
	f.srv.listMessages(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice/messages", nil), "carol"), "sess_alice")
	var fullPayload struct {
		Messages []store.Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fullPayload); err != nil {
		t.Fatalf("decode full: %v", err)
	}
	sawTrace := false
	for _, message := range fullPayload.Messages {
		if message.Trace != nil {
			sawTrace = true
		}
	}
	if !sawTrace {
		t.Fatal("full visibility must keep the execution trace")
	}
	if len(fullPayload.Messages) != 4 {
		t.Fatalf("full visibility should keep all 4 messages, got %d", len(fullPayload.Messages))
	}
}

func TestCreateShareValidation(t *testing.T) {
	f := newShareFixture(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "missing grantee", body: `{"visibility":"full"}`, want: http.StatusBadRequest},
		{name: "self share", body: `{"grantee":"alice"}`, want: http.StatusBadRequest},
		{name: "unknown user", body: `{"grantee":"nobody"}`, want: http.StatusBadRequest},
		{name: "bad visibility", body: `{"grantee":"bob","visibility":"everything"}`, want: http.StatusBadRequest},
		{name: "default visibility", body: `{"grantee":"bob"}`, want: http.StatusCreated},
		{name: "duplicate", body: `{"grantee":"bob"}`, want: http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := asUser(httptest.NewRequest(http.MethodPost, "/api/sessions/sess_alice/shares", strings.NewReader(tc.body)), "alice")
			f.srv.createSessionShare(rec, req, "sess_alice")
			if rec.Code != tc.want {
				t.Fatalf("want %d got %d (%s)", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
	// An identity share defaults to full: inside the org, seeing the work is the point.
	shares, err := f.store.ListSessionShares(context.Background(), "sess_alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(shares) != 1 || shares[0].Visibility != store.ShareVisibilityFull {
		t.Fatalf("expected one full-visibility share, got %+v", shares)
	}
}

func TestListSessionsAnnotatesAccess(t *testing.T) {
	f := newShareFixture(t)
	f.grant(t, "bob", store.ShareVisibilityFull)

	rec := httptest.NewRecorder()
	f.srv.listSessions(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions", nil), "bob"))
	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Sessions) != 1 {
		t.Fatalf("expected bob to see 1 shared session, got %d", len(payload.Sessions))
	}
	entry := payload.Sessions[0]
	if entry["access"] != "reader" || entry["shared_by"] != "alice" {
		t.Fatalf("unexpected annotation: %v", entry)
	}
	if _, present := entry["user_id"]; present {
		t.Fatalf("shared listing must not carry user_id: %v", entry)
	}

	rec = httptest.NewRecorder()
	f.srv.listSessions(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions", nil), "alice"))
	var ownerPayload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ownerPayload); err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	if len(ownerPayload.Sessions) != 1 || ownerPayload.Sessions[0]["access"] != "owner" {
		t.Fatalf("expected alice's own session marked owner: %v", ownerPayload.Sessions)
	}
	if ownerPayload.Sessions[0]["user_id"] != "alice" {
		t.Fatalf("owner listing must keep user_id: %v", ownerPayload.Sessions[0])
	}
}

// newArtifactShareFixture is newShareFixture with a real provisioned workspace,
// so the artifact path's VerifySessionWorkspaceVolume evidence check is exercised
// rather than bypassed.
func newArtifactShareFixture(t *testing.T) (*shareFixture, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := testArtifactDownloadConfig(t, dir)
	cfg.CookieName = "harness_auth"
	cfg.AuthSessionTTL = time.Hour
	cfg.AuthUsers = []config.AuthUser{
		{Username: "alice", PasswordHash: mustHash(t, "pw")},
		{Username: "bob", PasswordHash: mustHash(t, "pw")},
		{Username: "carol", PasswordHash: mustHash(t, "pw")},
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		t.Fatalf("mkdir db state: %v", err)
	}
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	session := store.Session{
		ID: "sess_alice", UserID: "alice", Status: string(sessionstate.Created),
		Agent: "pi", Mode: "agent", Workspace: filepath.Join(dir, "sessions", "sess_alice"),
		RestoreID: "r1", CreatedAt: now, UpdatedAt: now,
	}
	if err := os.MkdirAll(session.Workspace, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	if err := st.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv := &Server{
		cfg: cfg, store: st, hub: events.NewHub(), log: slog.Default(),
		creds:             newAuthRegistry(cfg, slog.Default()),
		sessionOwnerCache: map[string]sessionOwnerEntry{},
	}
	volumeConfig, err := srv.dataVolumeProvisionerConfig()
	if err != nil {
		t.Fatalf("data volume config: %v", err)
	}
	workspace, err := (localworkspace.Volumes{Records: st}).ProvisionSessionWorkspace(ctx, localworkspace.ProvisionSessionWorkspaceParams{
		SessionID: session.ID,
		Config:    volumeConfig,
		Now:       now,
	})
	if err != nil {
		t.Fatalf("provision workspace: %v", err)
	}
	return &shareFixture{srv: srv, store: st, session: session, dir: dir}, workspace.HostPath
}

// A reader may download a workspace file; the same grant must not let them PUT.
func TestArtifactReadGateAllowsGetAndRefusesPut(t *testing.T) {
	f, workspace := newArtifactShareFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("shared bytes"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	f.grant(t, "bob", store.ShareVisibilityFull)

	rec := httptest.NewRecorder()
	f.srv.getArtifact(rec, asUser(httptest.NewRequest(http.MethodGet, "/artifacts/sess_alice/report.txt", nil), "bob"))
	if rec.Code != http.StatusOK {
		t.Fatalf("reader download: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "shared bytes") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	put := asUser(httptest.NewRequest(http.MethodPut, "/artifacts/sess_alice/report.txt", strings.NewReader("overwritten")), "bob")
	f.srv.writeArtifact(rec, put)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reader PUT: want 404 got %d (%s)", rec.Code, rec.Body.String())
	}
	contents, err := os.ReadFile(filepath.Join(workspace, "report.txt"))
	if err != nil {
		t.Fatalf("reread artifact: %v", err)
	}
	if string(contents) != "shared bytes" {
		t.Fatalf("reader overwrote the file: %q", contents)
	}

	// An ungranted user sees nothing.
	rec = httptest.NewRecorder()
	f.srv.getArtifact(rec, asUser(httptest.NewRequest(http.MethodGet, "/artifacts/sess_alice/report.txt", nil), "carol"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ungranted download: want 404 got %d", rec.Code)
	}
}

func TestListUsersExcludesCallerAndIsEmptyInOpenMode(t *testing.T) {
	f := newShareFixture(t)
	rec := httptest.NewRecorder()
	f.srv.listUsers(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/users", nil), "alice"))
	var payload struct {
		Users []struct {
			UserID string `json:"user_id"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make([]string, 0, len(payload.Users))
	for _, user := range payload.Users {
		got = append(got, user.UserID)
	}
	if strings.Join(got, ",") != "bob,carol" {
		t.Fatalf("expected bob,carol (caller excluded, sorted), got %v", got)
	}

	// Open mode: no credentials file, so no named users to share with.
	f.srv.creds = newAuthRegistry(config.Config{}, f.srv.log)
	rec = httptest.NewRecorder()
	f.srv.listUsers(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/users", nil), "lab"))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"users":[]}` {
		t.Fatalf("expected empty user list in open mode, got %s", body)
	}
}

// Sharing notifies the recipient and nobody else.
func TestCreateShareNotifiesOnlyTheGrantee(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	// A pre-existing broadcast, to prove directed rows are additive rather than
	// replacing the broadcast semantics.
	if _, err := f.srv.CreateNotification(ctx, "Release notes", "for everyone"); err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	f.grant(t, "bob", store.ShareVisibilityFull)

	titles := func(userID string) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		f.srv.listNotifications(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/notifications", nil), userID))
		if rec.Code != http.StatusOK {
			t.Fatalf("list notifications for %s: %d", userID, rec.Code)
		}
		var payload struct {
			Notifications []apiNotification `json:"notifications"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make([]string, 0, len(payload.Notifications))
		for _, n := range payload.Notifications {
			out = append(out, n.Title)
		}
		return out
	}

	bob := strings.Join(titles("bob"), "|")
	if !strings.Contains(bob, "A session was shared with you") || !strings.Contains(bob, "Release notes") {
		t.Fatalf("bob should see his share notice and the broadcast, got %q", bob)
	}
	carol := strings.Join(titles("carol"), "|")
	if strings.Contains(carol, "shared with you") {
		t.Fatalf("carol must not see bob's share notice, got %q", carol)
	}
	if !strings.Contains(carol, "Release notes") {
		t.Fatalf("carol should still see the broadcast, got %q", carol)
	}
	alice := strings.Join(titles("alice"), "|")
	if strings.Contains(alice, "shared with you") {
		t.Fatalf("the owner must not be told they shared with themselves, got %q", alice)
	}
}
