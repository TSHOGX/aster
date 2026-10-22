// Public browser gateway never mounts the private participant/worker APIs.
package appidentity

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
)

type Gateway struct {
	ApplicationID string
	app           Application
	SessionView   func(Session) any
	Sessions      *Sessions
	PublicOrigin  string
	Frontend      string
	API           func(Session, http.ResponseWriter, *http.Request)
}

func (g Gateway) Handler() (http.Handler, error) {
	var ok bool
	g.app, ok = ApplicationFor(g.ApplicationID)
	if !ok {
		return nil, fmt.Errorf("unknown gateway application")
	}
	if g.Sessions == nil || g.Sessions.client.config.ClientID != g.ApplicationID || g.Sessions.client.config.Issuer != g.PublicOrigin {
		return nil, fmt.Errorf("gateway and session authority must name the same application and issuer")
	}
	browserAPIPrefix := g.app.Prefix + "api/v1/"
	public, err := url.Parse(g.PublicOrigin)
	if err != nil || public.Scheme != "https" || public.Hostname() == "" || public.Path != "" || public.User != nil || public.RawQuery != "" || public.ForceQuery || public.Fragment != "" {
		return nil, fmt.Errorf("application requires canonical public HTTPS origin and central sessions")
	}
	upstream, err := url.Parse(g.Frontend)
	if err != nil || upstream.Hostname() == "" || upstream.User != nil || upstream.Path != "" || upstream.RawQuery != "" || upstream.ForceQuery || upstream.Fragment != "" {
		return nil, fmt.Errorf("invalid application frontend origin")
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
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		writeBrowserError(w, 503, "frontend_unavailable")
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.EqualFold(r.Host, public.Host) {
			writeBrowserError(w, 421, "unrecognized_host")
			return
		}
		if !strings.HasPrefix(r.URL.Path, g.app.Prefix) {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, g.app.Prefix+"auth/") {
			w.Header().Set("Cache-Control", "private, no-store")
			switch r.URL.Path {
			case g.app.Prefix + "auth/login":
				if r.Method != http.MethodGet {
					w.WriteHeader(405)
					return
				}
				g.login(w, r)
			case g.app.Prefix + "auth/callback":
				if r.Method != http.MethodGet {
					w.WriteHeader(405)
					return
				}
				g.callback(w, r)
			case g.app.Prefix + "auth/logout":
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
		if strings.HasPrefix(r.URL.Path, browserAPIPrefix) {
			w.Header().Set("Cache-Control", "private, no-store")
			if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != g.PublicOrigin {
				writeBrowserError(w, 403, "origin_refused")
				return
			}
			session, err := g.Sessions.Authorize(r.Context(), browserCookieValue(r, g.app.SessionCookie()))
			if err != nil {
				browserIdentityError(w, err)
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if r.Header.Get("Origin") != g.PublicOrigin || !session.ValidCSRF(r.Header.Get("X-CSRF-Token")) {
					writeBrowserError(w, 403, "csrf_refused")
					return
				}
			}
			if r.URL.Path == browserAPIPrefix+"session" && r.Method == "GET" {
				var body any = session
				if g.SessionView != nil {
					body = g.SessionView(session)
				}
				write(w, 200, body)
				return
			}
			if g.API != nil {
				g.API(session, w, r)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, g.app.Prefix+"internal/") || strings.HasPrefix(r.URL.Path, g.app.Prefix+"provider/") || strings.HasPrefix(r.URL.Path, g.app.Prefix+"distribution/") {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}
func (g Gateway) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeBrowserError(w, 403, "origin_refused")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 || len(q["return"]) > 1 {
		writeBrowserError(w, 400, "invalid_login_request")
		return
	}
	for key := range q {
		if key != "return" {
			writeBrowserError(w, 400, "invalid_login_request")
			return
		}
	}
	cookie, location, err := g.Sessions.Begin(r.Context(), q.Get("return"))
	if err != nil {
		browserIdentityError(w, err)
		return
	}
	g.setBrowserCookie(w, g.app.LoginCookie(), cookie, 300)
	http.Redirect(w, r, location, http.StatusSeeOther)
}
func (g Gateway) callback(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["state"]) != 1 || len(q["code"]) != 1 {
		writeBrowserError(w, 400, "invalid_callback")
		return
	}
	token, destination, err := g.Sessions.Complete(r.Context(), browserCookieValue(r, g.app.LoginCookie()), q.Get("state"), q.Get("code"))
	g.setBrowserCookie(w, g.app.LoginCookie(), "", -1)
	if err != nil {
		browserIdentityError(w, err)
		return
	}
	g.setBrowserCookie(w, g.app.SessionCookie(), token, 7*24*3600)
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
func (g Gateway) logout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != g.PublicOrigin {
		writeBrowserError(w, 403, "origin_refused")
		return
	}
	if err := g.Sessions.Logout(r.Context(), browserCookieValue(r, g.app.SessionCookie()), r.Header.Get("X-CSRF-Token")); err != nil {
		browserIdentityError(w, err)
		return
	}
	g.setBrowserCookie(w, g.app.SessionCookie(), "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func browserCookieValue(r *http.Request, name string) string {
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
func (g Gateway) setBrowserCookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: g.app.Prefix, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func writeBrowserError(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}
func browserIdentityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthorized):
		writeBrowserError(w, 401, "login_required")
	case errors.Is(err, ErrUnavailable):
		writeBrowserError(w, 503, "identity_unavailable")
	default:
		writeBrowserError(w, 500, "internal_error")
	}
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
