package main

import (
	"aster/contracts/appidentity"
	"aster/evaluation-space/internal/projections"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserGatewayPersistsSessionsAndSeparatesPrivateRoutes(t *testing.T) {
	ctx := context.Background()
	var centralState atomic.Int32
	var groupState atomic.Int32
	challenge := ""
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "evaluation-space" || password != strings.Repeat("s", 32) || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
			t.Error("ambient or wrong service credentials")
			w.WriteHeader(403)
			return
		}
		if centralState.Load() == 1 {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/api/identity/v1/revoke" {
			centralState.Store(2)
			w.WriteHeader(204)
			return
		}
		if centralState.Load() == 2 {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/api/identity/v1/evaluation-space-access" {
			var input appidentity.EvaluationAccessRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Grant != strings.Repeat("g", 43) || input.Action != "config.read" || !input.ValidScope() {
				t.Error("invalid scoped authority request")
				w.WriteHeader(400)
				return
			}
			if groupState.Load() == 1 || input.GroupID == "foreign" {
				w.WriteHeader(403)
				return
			}
			if groupState.Load() == 2 {
				w.WriteHeader(503)
				return
			}
			result := appidentity.EvaluationAccess{Subject: "member", ExpiresAt: time.Now().Add(time.Minute), Items: []appidentity.EvaluationGroup{{ID: "group", Name: "Team", State: "unconfigured"}}, NextAfter: "group"}
			if groupState.Load() == 3 {
				result.Subject = "other"
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		var input map[string]string
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid body")
			return
		}
		assertion := appidentity.Assertion{Issuer: "https://app", Subject: "member", CentralSession: "central", Epoch: 1, Entitlements: []string{"evaluation_space.access"}, ExpiresAt: time.Now().Add(time.Minute)}
		if r.URL.Path == "/api/identity/v1/token" {
			hash := sha256.Sum256([]byte(input["code_verifier"]))
			if input["redirect_uri"] != "https://app/evaluation-space/auth/callback" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				t.Error("callback or PKCE mismatch")
				w.WriteHeader(403)
				return
			}
			assertion.Grant = strings.Repeat("g", 43)
		} else if input["grant"] != strings.Repeat("g", 43) {
			t.Error("grant mismatch")
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(assertion)
	}))
	defer central.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Project-Account-ID") != "" {
			t.Error("frontend received authority")
		}
		w.Header().Set("X-Frontend-Path", r.URL.Path)
		w.WriteHeader(200)
	}))
	defer frontend.Close()
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	makeHandler := func() http.Handler {
		server, listener, err := browserServer(store, "127.0.0.1:0", "https://app", frontend.URL, central.URL, strings.Repeat("s", 32))
		if err != nil {
			t.Fatal(err)
		}
		listener.Close()
		return server.Handler
	}
	handler := makeHandler()
	call := func(method, path string, cookies []*http.Cookie, origin, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://app"+path, nil)
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Authorization", "Bearer untrusted")
		r.Header.Set("X-Project-Account-ID", "forged")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/volumes-studio/", "/evaluation-space-other/", "/evaluation-space/internal/v1/generation/admit", "/evaluation-space/provider/v1/inputs"} {
		if w := call("GET", path, nil, "", ""); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/evaluation-space/", "/evaluation-space/groups/group/runs/run", "/evaluation-space/_next/static/app.js"} {
		if w := call("GET", path, []*http.Cookie{{Name: "project_login", Value: "central"}}, "", ""); w.Code != 200 || w.Header().Get("X-Frontend-Path") != path {
			t.Fatal(path, w.Code)
		}
	}
	if w := call("GET", "/evaluation-space/api/v1/session", nil, "", ""); w.Code != 401 {
		t.Fatal("anonymous API", w.Code)
	}
	for _, target := range []string{"//evil/", "/volumes-studio/", "/evaluation-space/%2e%2e/admin", "/evaluation-space/auth/callback"} {
		if w := call("GET", "/evaluation-space/auth/login?return="+url.QueryEscape(target), nil, "", ""); w.Code != 401 {
			t.Fatal("return escaped", target, w.Code)
		}
	}
	login := call("GET", "/evaluation-space/auth/login?return=/evaluation-space/groups/group/runs/run", nil, "", "")
	if login.Code != 303 {
		t.Fatal(login.Code, login.Body.String())
	}
	location, _ := url.Parse(login.Header().Get("Location"))
	challenge = location.Query().Get("code_challenge")
	if location.Query().Get("client_id") != "evaluation-space" {
		t.Fatal(location)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Secure-evaluation_space_login" || cookies[0].Path != "/evaluation-space/" || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal(cookies)
	}
	callback := "/evaluation-space/auth/callback?" + url.Values{"state": {location.Query().Get("state")}, "code": {strings.Repeat("c", 43)}}.Encode()
	complete := call("GET", callback, cookies, "", "")
	if complete.Code != 303 || complete.Header().Get("Location") != "/evaluation-space/groups/group/runs/run" {
		t.Fatal(complete.Code, complete.Body.String())
	}
	var app *http.Cookie
	for _, cookie := range complete.Result().Cookies() {
		if cookie.Name == "__Secure-evaluation_space_session" {
			app = cookie
		}
	}
	if app == nil || app.Value == cookies[0].Value {
		t.Fatal("session fixation")
	}
	if w := call("GET", callback, cookies, "", ""); w.Code != 401 {
		t.Fatal("callback replay", w.Code)
	}
	read := func() *httptest.ResponseRecorder {
		return call("GET", "/evaluation-space/api/v1/session", []*http.Cookie{app}, "", "")
	}
	var session appidentity.Session
	w := read()
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Subject != "member" || session.CSRF == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, endpoint := range []string{"/evaluation-space/api/v1/groups?limit=1", "/evaluation-space/api/v1/groups/group"} {
		w := call("GET", endpoint, []*http.Cookie{app}, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"group"`) || strings.Contains(w.Body.String(), `"subject"`) || strings.Contains(w.Body.String(), strings.Repeat("g", 43)) {
			t.Fatal("invalid browser group response", w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=1&limit=2", "subject=other", "grant=forged", "after=%2Fforeign", "search=%ZZ"} {
		if w := call("GET", "/evaluation-space/api/v1/groups?"+query, []*http.Cookie{app}, "", ""); w.Code != 400 {
			t.Fatal("invalid query accepted", query, w.Code)
		}
	}
	if w := call("GET", "/evaluation-space/api/v1/groups/foreign", []*http.Cookie{app}, "", ""); w.Code != 403 {
		t.Fatal("foreign group", w.Code)
	}
	for _, check := range []struct {
		state  int32
		status int
	}{{1, 403}, {2, 503}, {3, 403}} {
		groupState.Store(check.state)
		if w := call("GET", "/evaluation-space/api/v1/groups/group", []*http.Cookie{app}, "", ""); w.Code != check.status {
			t.Fatal("stale or substituted group authority", check.state, w.Code)
		}
	}
	groupState.Store(0)
	for _, path := range []string{"/evaluation-space/api/v1/catalog/service", "/evaluation-space/api/v1/workflows/secret/draft"} {
		if w := call("GET", path, []*http.Cookie{app}, "", ""); w.Code != 404 {
			t.Fatal("private handler mounted publicly", path, w.Code)
		}
	}
	if w := call("GET", "/evaluation-space/api/v1/session", []*http.Cookie{app, app}, "", ""); w.Code != 401 {
		t.Fatal("duplicate cookie", w.Code)
	}
	if w := call("POST", "/evaluation-space/api/v1/groups/group/runs/run/control", []*http.Cookie{app}, "https://app", ""); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler = makeHandler()
	if w := read(); w.Code != 200 {
		t.Fatal("session lost on restart", w.Code)
	}
	centralState.Store(1)
	if w := read(); w.Code != 503 {
		t.Fatal("authority cached through outage", w.Code)
	}
	centralState.Store(2)
	if w := read(); w.Code != 401 {
		t.Fatal("revoked session", w.Code)
	}
	if w := call("POST", "/evaluation-space/auth/logout", []*http.Cookie{app}, "https://app", session.CSRF); w.Code != 204 {
		t.Fatal("revoked user cannot logout", w.Code, w.Body.String())
	}
	centralState.Store(0)
	if w := read(); w.Code != 401 {
		t.Fatal("local logout not persisted", w.Code)
	}
}
