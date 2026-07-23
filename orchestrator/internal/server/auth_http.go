package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"
)

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.applyHSTS(w, r)
		if !s.authEnabled {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, labUserID)))
			return
		}
		cookie, err := r.Cookie(s.cookieName(r))
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		userID, err := s.store.LookupAuthSession(r.Context(), hashAuthToken(cookie.Value), time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, userID)))
	})
}

// userIDFromContext returns the authenticated user id injected by requireAuth.
// In open mode this is labUserID.
func userIDFromContext(ctx context.Context) string {
	if userID, ok := ctx.Value(userContextKey{}).(string); ok && userID != "" {
		return userID
	}
	return labUserID
}

// requestIsHTTPS reports whether the original client request used TLS, honoring
// a trusted X-Forwarded-Proto from a TLS-terminating reverse proxy.
func (s *Server) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg.TrustForwardedProto {
		return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
	}
	return false
}

func (s *Server) applyHSTS(w http.ResponseWriter, r *http.Request) {
	if s.requestIsHTTPS(r) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

// cookieName uses the __Host- prefix when cookies are Secure (which requires
// Secure + Path=/ + no Domain), and a plain name otherwise (e.g. local http).
func (s *Server) cookieName(r *http.Request) string {
	if s.useSecureCookie(r) {
		return "__Host-" + s.cfg.CookieName
	}
	return s.cfg.CookieName
}

func (s *Server) useSecureCookie(r *http.Request) bool {
	return s.cfg.CookieSecure && s.requestIsHTTPS(r)
}

func (s *Server) authCookie(r *http.Request, token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     s.cookieName(r),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.useSecureCookie(r),
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
	}
}

func (s *Server) expireCookie(r *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     s.cookieName(r),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.useSecureCookie(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	}
}

func newAuthToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func hashAuthToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// clientIP returns the client address used for rate-limit keying and logging.
//
// The orchestrator serves plain HTTP behind a TLS-terminating reverse proxy
// (see docs/operations.md). When TrustProxyHeaders is set (the default deployed
// posture), the trusted proxy is nginx, which OVERWRITES X-Real-IP with the true
// TCP peer ($remote_addr) — a value the external client cannot forge — so we use
// it directly. X-Forwarded-For is deliberately NOT trusted from the left: nginx
// APPENDS the real peer to the end of any client-supplied chain, so the leftmost
// entry is fully attacker-controlled and using it would let anyone bypass the
// per-IP login lockout by rotating a spoofed header. As a fallback we take the
// rightmost (last-hop) XFF entry, which nginx appended.
//
// When TrustProxyHeaders is false (orchestrator exposed directly, no trusted
// proxy), all client-supplied forwarding headers are ignored and only the real
// transport peer (RemoteAddr) is used.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
		if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
			// Use the rightmost entry: the hop appended by the trusted proxy.
			if comma := strings.LastIndexByte(fwd, ','); comma >= 0 {
				return strings.TrimSpace(fwd[comma+1:])
			}
			return fwd
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Request-body size caps. Bounded reads keep a single oversized or slow body
// from exhausting memory; the small cap covers control endpoints with tiny
// JSON, the message cap covers user-authored agent prompts (nginx allows up to
// 50m, but the orchestrator has no reason to buffer that much JSON in memory).
