package frontdoor

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T, studio, app http.Handler) http.Handler {
	t.Helper()
	s := httptest.NewServer(studio)
	t.Cleanup(s.Close)
	a := httptest.NewServer(app)
	t.Cleanup(a.Close)
	h, err := New(Config{PublicOrigin: "https://example.com", Studio: s.URL, Application: a.URL})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func request(h http.Handler, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://example.com"+target, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRoutingQueryRangesAndCookies(t *testing.T) {
	upstream := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream", name)
			w.Header().Set("X-Path", r.URL.EscapedPath())
			w.Header().Set("X-Query", r.URL.RawQuery)
			w.Header().Set("X-Cookies", r.Header.Get("Cookie"))
			w.Header().Set("X-Range", r.Header.Get("Range"))
			w.Header().Set("X-Conditional", r.Header.Get("If-None-Match"))
			w.Header().Set("X-Origin", r.Header.Get("Origin"))
			w.Header().Set("X-Request", r.Header.Get("X-Request-ID"))
			if strings.HasSuffix(r.URL.Path, "/missing") {
				w.WriteHeader(404)
			}
		})
	}
	h := setup(t, upstream("studio"), upstream("application"))
	for _, target := range []string{"/volumes-studio/", "/volumes-studio/_next/static/a.js", "/volumes-studio/libraries/one?x=a%2Fb&x=2", "/volumes-studio/api/v1/blob", "/api/admin", "/admin", "/volumes-studio-other", "/"} {
		r := httptest.NewRequest("GET", "https://example.com"+target, nil)
		r.Header.Set("Cookie", "project_login=central; "+SessionCookie+"=studio; "+LoginCookie+"=attempt; other=ignored")
		r.Header.Set("Range", "bytes=10-20")
		r.Header.Set("If-None-Match", `"digest"`)
		r.Header.Set("Origin", "https://example.com")
		r.Header.Set("X-Request-ID", "request-1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		isStudio := strings.HasPrefix(target, Prefix)
		want, cookies := "application", "project_login=central"
		if isStudio {
			want, cookies = "studio", SessionCookie+"=studio; "+LoginCookie+"=attempt"
		}
		if w.Code != 200 || w.Header().Get("X-Upstream") != want || w.Header().Get("X-Cookies") != cookies {
			t.Fatalf("%s: %d %v", target, w.Code, w.Header())
		}
		if w.Header().Get("X-Path") != r.URL.EscapedPath() || w.Header().Get("X-Query") != r.URL.RawQuery || w.Header().Get("X-Range") != "bytes=10-20" || w.Header().Get("X-Conditional") != `"digest"` || w.Header().Get("X-Origin") != "https://example.com" || w.Header().Get("X-Request") != "request-1" {
			t.Fatalf("request semantics changed: %v", w.Header())
		}
	}
	w := request(h, "/volumes-studio?x=a%2Fb")
	if w.Code != 308 || w.Header().Get("Location") != "/volumes-studio/?x=a%2Fb" {
		t.Fatalf("redirect: %d %v", w.Code, w.Header())
	}
	if request(h, "/volumes-studio/missing").Code != 404 {
		t.Fatal("upstream 404 hidden")
	}
}

func TestRejectAmbiguousRoutesAndCookieAuthority(t *testing.T) {
	h := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected studio request") }), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected application request") }))
	for _, target := range []string{"/volumes-studio/../api/admin", "//volumes-studio/", "/volumes-studio%2fapi/v1", "/volumes-studio/a%5cb", "/volumes-studio/a%00b"} {
		if got := request(h, target).Code; got != 400 {
			t.Errorf("%s: %d", target, got)
		}
	}
	for _, target := range []string{"/distribution", "/distribution/v1/exports", "/volumes-studio/distribution/v1/exports"} {
		if got := request(h, target).Code; got != 404 {
			t.Errorf("%s: %d", target, got)
		}
	}
	r := httptest.NewRequest("GET", "https://attacker.example/volumes-studio/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal(w.Code)
	}
	for _, pair := range [][2]string{{Prefix, SessionCookie}, {"/api/auth/session", "project_login"}} {
		r := httptest.NewRequest("GET", "https://example.com"+pair[0], nil)
		r.Header.Set("Cookie", pair[1]+"=first; "+pair[1]+"=second")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}

func TestOverwriteForwardingAndIdentityHeaders(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"Authorization", "Proxy-Authorization", "Forwarded", "X-Project-Account-ID", "X-Studio-Subject", "X-Identity-Subject", "X-Auth-User", "Remote-User", "X-Forwarded-Secret"} {
			if r.Header.Get(key) != "" {
				t.Errorf("forwarded spoofable %s", key)
			}
		}
		if r.Host != "example.com" || r.Header.Get("X-Forwarded-Host") != "example.com" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-For") != "192.0.2.1" || r.Header.Get("X-Real-IP") != "192.0.2.1" {
			t.Errorf("invalid forwarding: %v", r.Header)
		}
	})
	h := setup(t, upstream, upstream)
	for _, target := range []string{Prefix, "/api/auth/session"} {
		r := httptest.NewRequest("GET", "https://example.com"+target, nil)
		for _, key := range []string{"Authorization", "Proxy-Authorization", "Forwarded", "X-Project-Account-ID", "X-Studio-Subject", "X-Identity-Subject", "X-Auth-User", "Remote-User", "X-Forwarded-Secret", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For", "X-Real-IP"} {
			r.Header.Set(key, "spoofed")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
}

func TestRejectUpstreamCookieScopeEscape(t *testing.T) {
	valid := SessionCookie + "=opaque; Path=" + Prefix + "; Secure; HttpOnly; SameSite=Lax"
	for _, cookie := range []string{valid, "project_login=spoofed; Path=/", strings.Replace(valid, "Path="+Prefix, "Path=/", 1), valid + "; Domain=example.com", strings.Replace(valid, "; Secure", "", 1), strings.Replace(valid, "; HttpOnly", "", 1), strings.Replace(valid, "SameSite=Lax", "SameSite=None", 1), "invalid cookie"} {
		t.Run(cookie, func(t *testing.T) {
			h := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Set-Cookie", cookie)
				_, _ = io.WriteString(w, "response")
			}), http.NotFoundHandler())
			w := request(h, Prefix)
			if cookie == valid {
				if w.Code != 200 || w.Header().Get("Set-Cookie") != valid {
					t.Fatal(w.Code, w.Header())
				}
				return
			}
			if w.Code != 502 || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("unsafe cookie forwarded", w.Code, w.Header())
			}
		})
	}
}

func TestSourceUnavailableDoesNotRouteThroughApplication(t *testing.T) {
	a := httptest.NewServer(http.NotFoundHandler())
	a.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "studio") }))
	defer s.Close()
	h, err := New(Config{PublicOrigin: "https://example.com", Studio: s.URL, Application: a.URL})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, Prefix); w.Code != 200 || w.Body.String() != "studio" {
		t.Fatal("Studio depends on application")
	}
	if request(h, "/admin").Code != 502 {
		t.Fatal("application outage hidden")
	}
	s.Close()
	if request(h, Prefix).Code != 502 {
		t.Fatal("Studio outage hidden")
	}
}

func TestStreamingAndCancellation(t *testing.T) {
	settled := make(chan struct{})
	h := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(settled)
	}), http.NotFoundHandler())
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", server.URL+Prefix+"api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "example.com"
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: ready\n" {
		t.Fatal("event buffered", line, err)
	}
	cancel()
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("upstream reader not canceled")
	}
}

func TestConfigurationRefusesUnsafeOrigins(t *testing.T) {
	for _, value := range []string{"", "http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?key=value", "https://example.com#fragment", "file:///tmp/socket"} {
		if _, err := New(Config{PublicOrigin: value, Studio: "http://127.0.0.1:8000", Application: "http://127.0.0.1:8001"}); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	if _, err := New(Config{PublicOrigin: "https://example.com", Studio: "http://remote.example.com", Application: "http://127.0.0.1:8001"}); err == nil {
		t.Fatal("remote plaintext accepted")
	}
}
