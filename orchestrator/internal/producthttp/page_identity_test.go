package producthttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/edge"
	"harness-platform/orchestrator/internal/product"
)

func TestPageIdentityComesFromCurrentCookieAndCannotBeForged(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "page-identity.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	authentication := NewAuthentication(store, Config{TrustForwardedHost: true})
	app := httptest.NewServer(authentication.Handler((&Server{Store: store, Auth: authentication}).Handler()))
	defer app.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Header.Get(edge.PageAccountHeader)) }))
	defer frontend.Close()
	appURL, err := url.Parse(app.URL)
	must(t, err)
	frontendURL, err := url.Parse(frontend.URL)
	must(t, err)
	handler, err := edge.New(edge.Config{Application: appURL, Frontend: frontendURL, AdminFrontend: frontendURL, PageAccount: authentication.Actor})
	must(t, err)
	browser := httptest.NewServer(handler)
	defer browser.Close()
	read := func(path string, cookie *http.Cookie) string {
		r, err := http.NewRequest("GET", browser.URL+path, nil)
		must(t, err)
		r.Header.Set(edge.PageAccountHeader, "forged-account")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, err := browser.Client().Do(r)
		must(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		must(t, err)
		if path == "/" && !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			t.Fatal("identity-bearing page could be cached")
		}
		return string(raw)
	}
	login := func(username, password string) *http.Cookie {
		r, err := http.NewRequest("POST", browser.URL+"/api/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
		must(t, err)
		r.Header.Set("Origin", browser.URL)
		response, err := browser.Client().Do(r)
		must(t, err)
		defer response.Body.Close()
		if response.StatusCode != 200 {
			raw, _ := io.ReadAll(response.Body)
			t.Fatal(response.StatusCode, string(raw))
		}
		return response.Cookies()[0]
	}
	if got := read("/", nil); got != "" {
		t.Fatal("anonymous client forged page identity", got)
	}
	cookie := login("admin", "permanent-admin-password")
	if got := read("/", cookie); got != admin.ID {
		t.Fatal("page identity differs from cookie", got)
	}
	for _, path := range []string{"/s/public", "/_next/static/chunk.js"} {
		if got := read(path, cookie); got != "" {
			t.Fatal("page identity escaped its document scope", path, got)
		}
	}
	must(t, store.RevokeAllLogins(ctx, cookie.Value))
	if got := read("/", cookie); got != "" {
		t.Fatal("revoked cookie selected account cache", got)
	}
	_, temporary, err := store.CreateAccount(ctx, admin.ID, "setup", "Setup", "user")
	must(t, err)
	cookie = login("setup", temporary)
	if got := read("/", cookie); got != "" {
		t.Fatal("restricted session selected preferences", got)
	}
}
