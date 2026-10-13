// Package httpapi is Studio's public fixed-prefix boundary. The distribution
// service is a separate handler/listener and never falls through this gateway.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"volume-studio/backend/internal/frontdoor"
	"volume-studio/backend/internal/identity"
)

const apiPrefix = "/volumes-studio/api/v1/"

type Gateway struct {
	Sessions     *identity.Sessions
	PublicOrigin string
	Frontend     string
	API          func(identity.Session, http.ResponseWriter, *http.Request)
}

func (g Gateway) Handler() (http.Handler, error) {
	public, err := url.Parse(g.PublicOrigin)
	if err != nil || public.Scheme != "https" || public.Hostname() == "" || public.Path != "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || g.Sessions == nil {
		return nil, fmt.Errorf("Studio requires canonical public HTTPS origin and central sessions")
	}
	upstream, err := url.Parse(g.Frontend)
	if err != nil || upstream.Hostname() == "" || upstream.User != nil || upstream.Path != "" || upstream.RawQuery != "" || upstream.Fragment != "" {
		return nil, fmt.Errorf("invalid Studio frontend origin")
	}
	if upstream.Scheme != "https" {
		ip := net.ParseIP(upstream.Hostname())
		if upstream.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote frontend requires HTTPS")
		}
	}
	proxy := &httputil.ReverseProxy{FlushInterval: -1, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: time.Minute}, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(upstream)
		p.Out.Host = public.Host
		p.Out.Header.Del("Cookie")
		p.Out.Header.Del("Authorization")
		p.Out.Header.Del("X-Project-Account-ID")
		p.Out.Header.Set("X-Forwarded-Host", public.Host)
		p.Out.Header.Set("X-Forwarded-Proto", "https")
	}, ModifyResponse: func(r *http.Response) error {
		if len(r.Header.Values("Set-Cookie")) != 0 {
			return fmt.Errorf("frontend cannot mint sessions")
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) { writeError(w, 503, "frontend_unavailable") }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.EqualFold(r.Host, public.Host) {
			writeError(w, 421, "unrecognized_host")
			return
		}
		if !strings.HasPrefix(r.URL.Path, frontdoor.Prefix) {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/volumes-studio/auth/") {
			w.Header().Set("Cache-Control", "private, no-store")
			switch r.URL.Path {
			case "/volumes-studio/auth/login":
				if r.Method != http.MethodGet {
					w.WriteHeader(405)
					return
				}
				g.login(w, r)
			case "/volumes-studio/auth/callback":
				if r.Method != http.MethodGet {
					w.WriteHeader(405)
					return
				}
				g.callback(w, r)
			case "/volumes-studio/auth/logout":
				if r.Method != http.MethodPost {
					w.WriteHeader(405)
					return
				}
				g.logout(w, r)
			default:
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, apiPrefix) {
			w.Header().Set("Cache-Control", "private, no-store")
			if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != g.PublicOrigin {
				writeError(w, 403, "origin_refused")
				return
			}
			session, err := g.Sessions.Authorize(r.Context(), cookieValue(r, frontdoor.SessionCookie))
			if err != nil {
				identityError(w, err)
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if r.Header.Get("Origin") != g.PublicOrigin || !session.ValidCSRF(r.Header.Get("X-CSRF-Token")) {
					writeError(w, 403, "csrf_refused")
					return
				}
			}
			if r.URL.Path == apiPrefix+"session" && r.Method == "GET" {
				writeJSON(w, 200, session)
				return
			}
			if g.API != nil {
				g.API(session, w, r)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/volumes-studio/distribution/") {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}
func (g Gateway) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, 403, "origin_refused")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 || len(q["return"]) > 1 {
		writeError(w, 400, "invalid_login_request")
		return
	}
	for key := range q {
		if key != "return" {
			writeError(w, 400, "invalid_login_request")
			return
		}
	}
	cookie, location, err := g.Sessions.Begin(r.Context(), q.Get("return"))
	if err != nil {
		identityError(w, err)
		return
	}
	setCookie(w, frontdoor.LoginCookie, cookie, 300)
	http.Redirect(w, r, location, http.StatusSeeOther)
}
func (g Gateway) callback(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["state"]) != 1 || len(q["code"]) != 1 {
		writeError(w, 400, "invalid_callback")
		return
	}
	token, destination, err := g.Sessions.Complete(r.Context(), cookieValue(r, frontdoor.LoginCookie), q.Get("state"), q.Get("code"))
	setCookie(w, frontdoor.LoginCookie, "", -1)
	if err != nil {
		identityError(w, err)
		return
	}
	setCookie(w, frontdoor.SessionCookie, token, 7*24*3600)
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
func (g Gateway) logout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != g.PublicOrigin {
		writeError(w, 403, "origin_refused")
		return
	}
	if err := g.Sessions.Logout(r.Context(), cookieValue(r, frontdoor.SessionCookie), r.Header.Get("X-CSRF-Token")); err != nil {
		identityError(w, err)
		return
	}
	setCookie(w, frontdoor.SessionCookie, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func cookieValue(r *http.Request, name string) string {
	value := ""
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			count++
			value = cookie.Value
		}
	}
	if count != 1 {
		return ""
	}
	return value
}
func setCookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: frontdoor.Prefix, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func identityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthorized):
		writeError(w, 401, "login_required")
	case errors.Is(err, identity.ErrUnavailable):
		writeError(w, 503, "identity_unavailable")
	default:
		writeError(w, 500, "internal_error")
	}
}
