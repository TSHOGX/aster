package producthttp

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/testfixture"
)

func TestCookieAuthenticationRevocationAndOrigin(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "Alice", "correct-password", identitySeed(t))
	must(t, err)
	authentication := NewAuthentication(store, Config{})
	handler := authentication.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := authentication.Actor(r)
		if err != nil {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, 200, map[string]string{"actor": actor})
	}))
	request := func(method, path, origin string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		r.Header.Set("Origin", origin)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	login := func(origin, password string) *httptest.ResponseRecorder {
		return request("POST", "/api/auth/login", origin, map[string]string{"username": "ALICE", "password": password}, nil)
	}
	for _, origin := range []string{"https://foreign", "http://app", "https://app/path", "https://user@app"} {
		if got := login(origin, "correct-password"); got.Code != 403 {
			t.Fatal(origin, got.Code)
		}
	}
	if got := login("https://app", "incorrect"); got.Code != 401 {
		t.Fatal(got.Code)
	}
	response := login("https://app", "correct-password")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal(cookie)
	}
	response = request("GET", "/private", "https://app", nil, cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), admin.ID) {
		t.Fatal(response.Code, response.Body.String())
	}
	response = request("POST", "/api/me/password", "https://foreign", map[string]string{"current_password": "correct-password", "new_password": "replacement-password"}, cookie)
	if response.Code != 403 {
		t.Fatal(response.Code)
	}
	response = request("POST", "/api/me/password", "https://app", map[string]string{"current_password": "correct-password", "new_password": "replacement-password"}, cookie)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	replacement := response.Result().Cookies()[0]
	if got := request("GET", "/private", "https://app", nil, cookie); got.Code != 401 {
		t.Fatal("old cookie survived", got.Code)
	}
	if got := request("GET", "/private", "https://app", nil, replacement); got.Code != 200 {
		t.Fatal("replacement refused", got.Code)
	}
	if got := request("DELETE", "/api/me/sessions", "https://app", nil, replacement); got.Code != 204 {
		t.Fatal(got.Code)
	}
	if got := request("GET", "/private", "https://app", nil, replacement); got.Code != 401 {
		t.Fatal("sign out all retained cookie")
	}
}

func TestRestrictedLoginOnlyAuthorizesSetupSessionAndLogout(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "correct-password", identitySeed(t))
	must(t, err)
	_, adminToken, err := store.Authenticate(ctx, admin.Username, "correct-password")
	must(t, err)
	created, err := store.CreateAccount(ctx, adminToken, product.AccountCreate{RequestID: "create-user", Username: "user", DisplayName: "User", Role: "user"})
	password := created.TemporaryPassword
	must(t, err)
	authentication := NewAuthentication(store, Config{})
	api := (&Server{Store: store, Auth: authentication}).Handler()
	handler := authentication.Handler(api)
	perform := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://app"+path, strings.NewReader(string(raw)))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	response := perform("POST", "/api/auth/login", map[string]string{"username": "user", "password": password}, nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	if cookie.MaxAge != 900 {
		t.Fatal(cookie.MaxAge)
	}
	for _, path := range []string{"/api/projects", "/api/users"} {
		if got := perform("GET", path, nil, cookie); got.Code != 401 {
			t.Fatal(path, got.Code)
		}
	}
	response = perform("GET", "/api/auth/session", nil, cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"must_change_password":true`) {
		t.Fatal(response.Code, response.Body.String())
	}
	response = perform("POST", "/api/auth/login", map[string]string{"username": "user", "password": password}, nil)
	if response.Code != 401 {
		t.Fatal("temporary credential reused", response.Code)
	}
	response = perform("POST", "/api/auth/password/setup", map[string]string{"new_password": "permanent-password"}, cookie)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	replacement := response.Result().Cookies()[0]
	response = perform("POST", "/api/auth/password/setup", map[string]string{"new_password": "duplicate-password"}, cookie)
	if response.Code != 401 {
		t.Fatal("duplicate setup", response.Code)
	}
	if got := perform("GET", "/api/projects", nil, replacement); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
}

// The runtime is scripted, but authentication, cookies, SQL, HTTP streaming and
// WebSocket transport are real. Revocation must end existing browser authority.
func TestDatabaseSessionRevocationClosesLiveStreamsAndTerminal(t *testing.T) {
	for _, change := range []string{"password", "all-sessions"} {
		t.Run(change, func(t *testing.T) {
			store, err := product.Open(ctx, filepath.Join(t.TempDir(), "session-revocation.db"))
			must(t, err)
			h := harnessForStore(t, store)
			account, err := h.store.BootstrapAccount(ctx, "owner", "owner-password-before", identitySeed(t))
			must(t, err)
			h.setActor(account.ID)
			h.terminal(t, "authenticated-project", "shell")
			_, token, err := h.store.Authenticate(ctx, "owner", "owner-password-before")
			must(t, err)
			authentication := NewAuthentication(h.store, Config{})
			api := &Server{Store: h.store, Terminals: h.terminals, Lifecycle: h.lifecycle, Profile: testProfile(), Auth: authentication}
			server := httptest.NewServer(authentication.Handler(api.Handler()))
			defer server.Close()
			defer api.Close()
			request, err := http.NewRequest("GET", server.URL+"/api/projects/authenticated-project/resources/events", nil)
			must(t, err)
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
			response, err := server.Client().Do(request)
			must(t, err)
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.StatusCode)
			}
			_, err = h.store.CreateSession(ctx, product.Scope{Actor: account.ID, ProjectID: "authenticated-project"}, "observed-conversation", "Observed")
			must(t, err)
			conversationRequest, err := http.NewRequest("GET", server.URL+"/api/conversations/observed-conversation/events", nil)
			must(t, err)
			conversationRequest.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
			conversationResponse, err := server.Client().Do(conversationRequest)
			must(t, err)
			defer conversationResponse.Body.Close()
			if conversationResponse.StatusCode != 200 {
				t.Fatal(conversationResponse.StatusCode)
			}
			conversationDone := make(chan struct{})
			go func() { io.Copy(io.Discard, conversationResponse.Body); close(conversationDone) }()
			streamDone := make(chan struct{})
			go func() { io.Copy(io.Discard, response.Body); close(streamDone) }()
			headers := http.Header{"Cookie": {loginCookie + "=" + token}, "Origin": {server.URL}}
			socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/projects/authenticated-project/terminals/shell/attach", headers)
			must(t, err)
			defer socket.Close()
			readText(t, socket, func(frame map[string]any) bool { return frame["kind"] == "status" })
			started := time.Now()
			replacement := ""
			if change == "password" {
				_, replacement, err = h.store.ChangePassword(ctx, token, "owner-password-before", "owner-password-after", false)
			} else {
				err = h.store.RevokeAllLogins(ctx, token)
			}
			must(t, err)
			must(t, socket.SetReadDeadline(started.Add(5*time.Second)))
			for {
				if _, _, err = socket.ReadMessage(); err != nil {
					break
				}
			}
			if time.Since(started) >= 5*time.Second {
				t.Fatal("terminal revocation missed five-second bound")
			}
			select {
			case <-streamDone:
			case <-time.After(time.Until(started.Add(5 * time.Second))):
				t.Fatal("stream revocation missed five-second bound")
			}
			select {
			case <-conversationDone:
			case <-time.After(time.Until(started.Add(5 * time.Second))):
				t.Fatal("conversation revocation missed five-second bound")
			}
			t.Logf("%s revoked SSE and terminal in %s", change, time.Since(started))
			live, err := h.store.LiveTerminal(ctx, product.Scope{Actor: account.ID, ProjectID: "authenticated-project"}, "shell")
			must(t, err)
			if live.State != "running" {
				t.Fatal("self-service revocation stopped retained work", live.State)
			}
			h.backend.mu.Lock()
			signals := len(h.backend.signals)
			h.backend.mu.Unlock()
			if signals != 0 {
				t.Fatal("self-service revocation signaled the retained process", signals)
			}
			if replacement != "" {
				if _, err = h.store.Login(ctx, replacement); err != nil {
					t.Fatal("replacement session invalid", err)
				}
			}
		})
	}
}

func identitySeed(t *testing.T) product.BootstrapSeed {
	t.Helper()
	manifest, digest := testfixture.RuntimeManifest()
	network, err := product.NewNetworkPolicy(product.Reachability{})
	must(t, err)
	seed, err := product.NewBootstrapSeed(product.ResourceSeed{CPU: 1000, Memory: 1 << 30, Overlay: 256 << 20, Workspace: 1 << 30, Runtime: 1 << 29, MaxMounts: 8, ProjectSlots: 4, AgentSlots: 32, TerminalSlots: 32}, product.RegisteredSandbox{Reference: product.ArtifactReference{ID: "test", Version: "v1", Digest: digest}, StoreID: "engine", TemplateID: "agent-runtime", Manifest: manifest}, nil, network)
	must(t, err)
	return seed
}

func TestTerminalSocketUsesCurrentTerminalGrant(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "terminal-policy.db"))
	must(t, err)
	h := harnessForStore(t, store)
	account, err := store.BootstrapAccount(ctx, "owner", "owner-password-before", identitySeed(t))
	must(t, err)
	h.setActor(account.ID)
	i := h.terminal(t, "policy-project", "shell")
	_, token, err := store.Authenticate(ctx, "owner", "owner-password-before")
	must(t, err)
	authentication := NewAuthentication(store, Config{})
	api := &Server{Store: store, Terminals: h.terminals, Lifecycle: h.lifecycle, Profile: testProfile(), Auth: authentication}
	server := httptest.NewServer(authentication.Handler(api.Handler()))
	defer server.Close()
	defer api.Close()
	policies, err := store.Policies(ctx, token, "", 50)
	must(t, err)
	if len(policies.Policies) != 1 {
		t.Fatal(policies)
	}
	policy := policies.Policies[0]
	policy.Statements[product.ActionProjectsRead] = product.PolicyDeny
	_, err = store.SavePolicy(ctx, token, policy.ID, product.PolicyChange{RequestID: "deny-project-read", ExpectedRevision: policy.Revision, Statements: &policy.Statements})
	must(t, err)
	headers := http.Header{"Cookie": {loginCookie + "=" + token}, "Origin": {server.URL}}
	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/projects/policy-project/terminals/shell/attach?input=1"
	socket, _, err := websocket.DefaultDialer.Dial(target, headers)
	must(t, err)
	defer socket.Close()
	readText(t, socket, func(frame map[string]any) bool { return frame["kind"] == "status" })
	// Exercise at least one periodic recheck with Project read denied and Terminal
	// use still allowed. Both the transport and service observer must remain live.
	time.Sleep(1200 * time.Millisecond)
	must(t, socket.WriteMessage(websocket.BinaryMessage, []byte("allowed")))
	h.backend.stream(i.Reference.Handle).frames <- sandbox.ProcessFrame{Kind: "data", Stream: "pty", Data: []byte("still connected"), Sequence: 2}
	must(t, socket.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		kind, data, err := socket.ReadMessage()
		must(t, err)
		if kind == websocket.BinaryMessage {
			if string(data) != "still connected" {
				t.Fatal(string(data))
			}
			break
		}
	}
	policy, err = store.Policy(ctx, token, policy.ID)
	must(t, err)
	policy.Statements[product.ActionTerminalsUse] = product.PolicyDeny
	started := time.Now()
	_, err = store.SavePolicy(ctx, token, policy.ID, product.PolicyChange{RequestID: "deny-terminal-use", ExpectedRevision: policy.Revision, Statements: &policy.Statements})
	must(t, err)
	must(t, socket.SetReadDeadline(started.Add(5*time.Second)))
	for {
		if _, _, err = socket.ReadMessage(); err != nil {
			break
		}
	}
	if time.Since(started) >= 5*time.Second {
		t.Fatal("terminal grant revocation missed five-second bound")
	}
	t.Logf("policy revocation closed terminal in %s", time.Since(started))
	_, response, err := websocket.DefaultDialer.Dial(target, headers)
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("revoked attachment accepted", response, err)
	}
	live, err := store.LiveTerminal(ctx, product.Scope{Actor: account.ID, ProjectID: "policy-project"}, "shell")
	must(t, err)
	if live.State != "running" {
		t.Fatal("view revocation ended process", live.State)
	}
	h.backend.mu.Lock()
	signals := len(h.backend.signals)
	h.backend.mu.Unlock()
	if signals != 0 {
		t.Fatal("policy revocation signaled PTY", signals)
	}
	h.terminals.TerminationGrace = time.Millisecond
	stop, err := http.NewRequest("POST", server.URL+"/api/projects/policy-project/terminals/shell/stop", nil)
	must(t, err)
	stop.Header.Set("Origin", server.URL)
	stop.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
	response, err = server.Client().Do(stop)
	must(t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatal("read/use denial blocked owned stop", response.StatusCode)
	}
}
