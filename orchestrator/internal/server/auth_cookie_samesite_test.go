package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/config"
)

// SameSite=None only reaches the browser intact when the cookie is also Secure —
// browsers reject None-without-Secure outright, which drops the auth cookie
// entirely instead of degrading to Strict. The gate therefore has to consider
// both the flag and whether the request was HTTPS, so every combination is
// pinned here: getting this wrong logs everyone out rather than failing loudly.
func TestCookieSameSiteRequiresSecure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sameSiteNone bool
		cookieSecure bool
		https        bool
		want         http.SameSite
	}{
		// The deployed posture: embeddable anywhere, so None over HTTPS.
		{"cross-site embed over https", true, true, true, http.SameSiteNoneMode},
		// http can't carry Secure, so None would be discarded by the browser.
		{"plain http stays strict", true, true, false, http.SameSiteStrictMode},
		// CookieSecure=false (local dev) means no Secure attribute at all.
		{"insecure cookies stay strict", true, false, true, http.SameSiteStrictMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: config.Config{
				CookieSameSiteNone:  tc.sameSiteNone,
				CookieSecure:        tc.cookieSecure,
				TrustForwardedProto: true,
			}}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.https {
				r.Header.Set("X-Forwarded-Proto", "https")
			}

			if got := s.cookieSameSite(r); got != tc.want {
				t.Fatalf("cookieSameSite = %v, want %v", got, tc.want)
			}

			// Both cookie builders must agree: a logout that writes a different
			// SameSite than login used leaves the original cookie in place.
			auth := s.authCookie(r, "tok", time.Now().Add(time.Hour))
			expire := s.expireCookie(r)
			if auth.SameSite != tc.want || expire.SameSite != tc.want {
				t.Fatalf("auth=%v expire=%v, want both %v", auth.SameSite, expire.SameSite, tc.want)
			}
			if auth.SameSite == http.SameSiteNoneMode && !auth.Secure {
				t.Fatal("SameSite=None without Secure: browsers discard this cookie")
			}
		})
	}
}
