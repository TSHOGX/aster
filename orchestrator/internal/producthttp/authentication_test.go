package producthttp

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/product"
)

func TestCookieAuthenticationRevocationAndOrigin(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	must(t, err)
	defer store.Close()
	hash, err := auth.HashPasswordWithIterations("correct", 1000)
	must(t, err)
	authentication, err := NewAuthentication(store, Config{}, []AuthUser{{Username: "alice", PasswordHash: hash}}, "")
	must(t, err)
	handler := authentication.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := authentication.Actor(r)
		if err != nil {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, 200, map[string]string{"actor": actor})
	}))
	login := func(origin, password string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "https://app/api/auth/login", strings.NewReader(`{"username":"alice","password":"`+password+`"}`))
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, origin := range []string{"https://foreign", "http://app", "https://app/path", "https://user@app"} {
		if got := login(origin, "correct"); got.Code != 403 {
			t.Fatal(origin, got.Code)
		}
	}
	if got := login("https://app", "incorrect"); got.Code != 401 {
		t.Fatal(got.Code)
	}
	result := login("https://app", "correct")
	if result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	cookies := result.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal(cookies)
	}
	request := httptest.NewRequest("GET", "https://app/private", nil)
	request.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	changed, err := auth.HashPasswordWithIterations("changed", 1000)
	must(t, err)
	must(t, authentication.ReplaceUsers([]AuthUser{{Username: "alice", PasswordHash: changed}}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("password rotation retained cookie")
	}
	result = login("https://app", "changed")
	if result.Code != 200 {
		t.Fatal(result.Code)
	}
	cookie := result.Result().Cookies()[0]
	logout := httptest.NewRequest("POST", "https://app/api/auth/logout", nil)
	logout.Header.Set("Origin", "https://app")
	logout.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, logout)
	if response.Code != 204 {
		t.Fatal(response.Code)
	}
	request = httptest.NewRequest("GET", "https://app/private", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("logout retained cookie")
	}
	if _, err = NewAuthentication(store, Config{}, nil, ""); err == nil {
		t.Fatal("missing credentials enabled open access")
	}
	if _, err = NewAuthentication(store, Config{}, []AuthUser{{Username: "alice", PasswordHash: hash}}, "local"); err == nil {
		t.Fatal("ambiguous authentication mode")
	}
}
