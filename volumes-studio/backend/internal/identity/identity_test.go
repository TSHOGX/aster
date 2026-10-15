package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Client, *atomic.Int32, *string) {
	t.Helper()
	var state atomic.Int32
	challenge := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "studio" || password != strings.Repeat("secret", 8) {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("ambient credentials sent")
		}
		if state.Load() == 2 {
			http.Error(w, "unavailable", 503)
			return
		}
		if state.Load() == 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path == "/api/identity/v1/revoke" {
			state.Store(1)
			w.WriteHeader(204)
			return
		}
		a := Assertion{Issuer: "https://example.com", Subject: "subject", CentralSession: "central-session", Epoch: 1, Entitlements: []string{AccessEntitlement}, ExpiresAt: time.Now().Add(time.Minute)}
		if r.URL.Path == "/api/identity/v1/token" {
			sum := sha256.Sum256([]byte(body["code_verifier"]))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || body["redirect_uri"] != "https://example.com/volumes-studio/auth/callback" {
				http.Error(w, "pkce", 401)
				return
			}
			a.Grant = strings.Repeat("g", 43)

		} else if body["grant"] != strings.Repeat("g", 43) {
			http.Error(w, "grant", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{Issuer: "https://example.com", Endpoint: server.URL, ClientID: "studio", ClientSecret: strings.Repeat("secret", 8)})
	if err != nil {
		t.Fatal(err)
	}
	return client, &state, &challenge
}
func login(t *testing.T, s *Sessions, challenge *string) string {
	t.Helper()
	cookie, location, err := s.Begin(context.Background(), "/volumes-studio/libraries/one")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(location)
	*challenge = u.Query().Get("code_challenge")
	state := u.Query().Get("state")
	if _, _, err = s.Complete(context.Background(), cookie, strings.Repeat("x", 43), strings.Repeat("c", 43)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("mismatched state accepted", err)
	}
	token, returnPath, err := s.Complete(context.Background(), cookie, state, strings.Repeat("c", 43))
	if err != nil {
		t.Fatal(err)
	}
	if token == cookie || returnPath != "/volumes-studio/libraries/one" {
		t.Fatal("session fixation or wrong return")
	}
	if _, _, err = s.Complete(context.Background(), cookie, state, strings.Repeat("c", 43)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("callback replay accepted", err)
	}
	return token
}
func TestDurableAppSessionFreshAuthorityAndLogout(t *testing.T) {
	client, state, challenge := fixture(t)
	name := filepath.Join(t.TempDir(), "sessions.db")
	ctx := context.Background()
	s, err := OpenSessions(ctx, name, client)
	if err != nil {
		t.Fatal(err)
	}
	token := login(t, s, challenge)
	session, err := s.Authorize(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if session.Subject != "subject" || !session.ValidCSRF(session.CSRF) || session.ValidCSRF("bad") {
		t.Fatal("session authority")
	}
	if err = s.Logout(ctx, token, "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("csrf accepted", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSessions(ctx, name, client)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Authorize(ctx, token); err != nil {
		t.Fatal("session lost across restart", err)
	}
	state.Store(2)
	if _, err = s.Authorize(ctx, token); !errors.Is(err, ErrUnavailable) {
		t.Fatal("outage extended permission", err)
	}
	if err = s.Logout(ctx, token, session.CSRF); !errors.Is(err, ErrUnavailable) {
		t.Fatal("logout outage hidden", err)
	}
	state.Store(0)
	if err = s.Logout(ctx, token, session.CSRF); err != nil {
		t.Fatal(err)
	}
	if state.Load() != 1 {
		t.Fatal("central logout not performed")
	}
	if _, err = s.Authorize(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("local logout failed", err)
	}
}
func TestExpiredAttemptAndNoSecondAccounts(t *testing.T) {
	client, _, _ := fixture(t)
	s, err := OpenSessions(context.Background(), filepath.Join(t.TempDir(), "sessions.db"), client)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	s.now = func() time.Time { return now }
	cookie, location, err := s.Begin(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(location)
	now = now.Add(5 * time.Minute)
	if _, _, err = s.Complete(context.Background(), cookie, u.Query().Get("state"), strings.Repeat("c", 43)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired attempt accepted", err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE '%account%' OR name LIKE '%password%'").Scan(&count); err != nil || count != 0 {
		t.Fatal("second account store", count, err)
	}
}
func TestLocalReturnsRefuseEscapes(t *testing.T) {
	for _, value := range []string{"https://evil.example/", "//evil.example/", "/admin", "/volumes-studio-other/", "/volumes-studio/../admin", "/volumes-studio/%2e%2e/admin", "/volumes-studio/\\evil", "/volumes-studio/%5cevil", "/volumes-studio/auth/callback", "/volumes-studio/a\n"} {
		if _, err := LocalReturn(value); err == nil {
			t.Fatal("unsafe return", value)
		}
	}
	if got, err := LocalReturn("/volumes-studio/libraries/one?view=tree"); err != nil || got != "/volumes-studio/libraries/one?view=tree" {
		t.Fatal(got, err)
	}
}
func TestClientRefusesRedirectAndWrongIssuer(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	c, err := NewClient(Config{Issuer: "https://example.com", Endpoint: server.URL, ClientID: "studio", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Introspect(context.Background(), "grant"); !errors.Is(err, ErrUnavailable) || requests.Load() != 0 {
		t.Fatal("followed credential redirect", err)
	}
	for _, endpoint := range []string{"http://remote.example", "https://user:password@example.com", "https://example.com/path"} {
		if _, err = NewClient(Config{Issuer: "https://example.com", Endpoint: endpoint, ClientID: "studio", ClientSecret: strings.Repeat("s", 32)}); err == nil {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
}
