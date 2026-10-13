package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"volume-studio/backend/internal/frontdoor"
	"volume-studio/backend/internal/identity"
)

func TestGatewayLoginCookieCSRFAndPrefix(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/identity/v1/revoke" {
			w.WriteHeader(204)
			return
		}
		_ = json.NewEncoder(w).Encode(identity.Assertion{Issuer: "https://example.com", Subject: "subject", CentralSession: "central", Epoch: 1, Entitlements: []string{identity.AuthorEntitlement}, ExpiresAt: time.Now().Add(time.Minute), Grant: strings.Repeat("g", 43)})
	}))
	defer central.Close()
	client, err := identity.NewClient(identity.Config{Issuer: "https://example.com", Endpoint: central.URL, ClientID: "studio", ClientSecret: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := identity.OpenSessions(context.Background(), filepath.Join(t.TempDir(), "sessions.db"), client)
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("session forwarded to frontend")
		}
		w.Header().Set("X-Frontend-Path", r.URL.Path)
		w.WriteHeader(200)
	}))
	defer frontend.Close()
	h, err := (Gateway{Sessions: sessions, PublicOrigin: "https://example.com", Frontend: frontend.URL, API: func(s identity.Session, w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"subject": s.Subject})
	}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	perform := func(method, path string, cookie *http.Cookie, origin, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://example.com"+path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/", "/api/admin", "/volumes-studio-other/", "/volumes-studio/distribution/v1/exports"} {
		if w := perform("GET", path, nil, "", ""); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/volumes-studio/", "/volumes-studio/libraries/one", "/volumes-studio/_next/static/app.js"} {
		w := perform("GET", path, &http.Cookie{Name: frontdoor.SessionCookie, Value: "not-forwarded"}, "", "")
		if w.Code != 200 || w.Header().Get("X-Frontend-Path") != path {
			t.Fatal(path, w.Code)
		}
	}
	if w := perform("GET", apiPrefix+"session", nil, "", ""); w.Code != 401 {
		t.Fatal("unauthenticated API", w.Code)
	}
	login := perform("GET", "/volumes-studio/auth/login?return=/volumes-studio/", nil, "", "")
	if login.Code != 303 {
		t.Fatal(login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("transient cookie missing")
	}
	cookie := cookies[0]
	if cookie.Name != frontdoor.LoginCookie || cookie.Path != frontdoor.Prefix || !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("invalid cookie scope")
	}
	location, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	callback := "/volumes-studio/auth/callback?" + url.Values{"state": {location.Query().Get("state")}, "code": {strings.Repeat("c", 43)}}.Encode()
	complete := perform("GET", callback, cookie, "", "")
	if complete.Code != 303 || complete.Header().Get("Location") != "/volumes-studio/" {
		t.Fatal(complete.Code, complete.Body.String())
	}
	var app *http.Cookie
	for _, c := range complete.Result().Cookies() {
		if c.Name == frontdoor.SessionCookie {
			app = c
		}
	}
	if app == nil {
		t.Fatal("app session cookie missing")
	}
	if w := perform("GET", callback, cookie, "", ""); w.Code != 401 {
		t.Fatal("callback replay", w.Code)
	}
	session := perform("GET", apiPrefix+"session", app, "", "")
	if session.Code != 200 {
		t.Fatal(session.Code)
	}
	var body identity.Session
	if err = json.Unmarshal(session.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CSRF == "" || strings.Contains(session.Body.String(), "grant") {
		t.Fatal("session projection")
	}
	for _, v := range [][2]string{{"https://evil.example", body.CSRF}, {"", body.CSRF}, {"https://example.com", "bad"}} {
		if w := perform("POST", apiPrefix+"libraries", app, v[0], v[1]); w.Code != 403 {
			t.Fatal("CSRF accepted", w.Code)
		}
	}
	if w := perform("POST", apiPrefix+"libraries", app, "https://example.com", body.CSRF); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := perform("GET", "/volumes-studio/auth/logout", app, "https://example.com", body.CSRF); w.Code != 405 {
		t.Fatal("GET logout")
	}
	if w := perform("POST", "/volumes-studio/auth/logout", app, "https://example.com", body.CSRF); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := perform("GET", apiPrefix+"session", app, "", ""); w.Code != 401 {
		t.Fatal("session survived logout")
	}
}
