// Package frontdoor routes the public origin without depending on either
// application's availability. It is a separate executable, not an Aster handler.
package frontdoor

import (
	"aster/contracts/appidentity"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"
)

const Prefix = "/volumes-studio/"
const SessionCookie = "__Secure-volumes_studio_session"
const LoginCookie = "__Secure-volumes_studio_login"

type Config struct {
	PublicOrigin    string
	Studio          string
	EvaluationSpace string
	Application     string
}

func origin(value string, public bool) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" || u.RawPath != "" {
		return nil, errors.New("origin must be an absolute HTTP(S) origin without credentials, path or query")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("origin must use HTTP(S)")
	}
	// Plaintext transport is confined to one host. Remote upstreams require TLS.
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTP origin must use a loopback IP; remote origins require HTTPS")
		}
	}
	if public && u.Scheme != "https" {
		return nil, errors.New("public origin requires HTTPS")
	}
	u.Path = ""
	return u, nil
}

func New(config Config) (http.Handler, error) {
	public, err := origin(config.PublicOrigin, true)
	if err != nil {
		return nil, err
	}
	studio, err := origin(config.Studio, false)
	if err != nil {
		return nil, err
	}
	application, err := origin(config.Application, false)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 128, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true,
	}
	studioProxy := proxy(studio, public, transport, Prefix)
	appProxy := proxy(application, public, transport, "")
	var evaluationProxy *httputil.ReverseProxy
	if config.EvaluationSpace != "" {
		evaluation, err := origin(config.EvaluationSpace, false)
		if err != nil {
			return nil, err
		}
		evaluationProxy = proxy(evaluation, public, transport, "/evaluation-space/")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, public.Host) {
			http.Error(w, "unrecognized public host", http.StatusMisdirectedRequest)
			return
		}
		// Reject ambiguous paths before different upstream normalizers can disagree
		// about the cookie/authority destination. Ordinary escaped UTF-8 is allowed.
		p := r.URL.Path
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || (p != path.Clean(p) && p != path.Clean(p)+"/") || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%2f") {
			http.Error(w, "noncanonical request path", http.StatusBadRequest)
			return
		}
		if p == "/distribution" || strings.HasPrefix(p, "/distribution/") || p == "/volumes-studio/distribution" || strings.HasPrefix(p, "/volumes-studio/distribution/") {
			http.NotFound(w, r)
			return
		}
		if p == strings.TrimSuffix(Prefix, "/") || p == "/evaluation-space" {
			location := p + "/"
			if r.URL.RawQuery != "" {
				location += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		appPrefix := ""
		if strings.HasPrefix(p, Prefix) {
			appPrefix = Prefix
		}
		if strings.HasPrefix(p, "/evaluation-space/") {
			appPrefix = "/evaluation-space/"
		}
		if err := filterCookies(r, appPrefix); err != nil {
			http.Error(w, "ambiguous session cookie", http.StatusBadRequest)
			return
		}
		if appPrefix == Prefix {
			studioProxy.ServeHTTP(w, r)
		} else if appPrefix == "/evaluation-space/" {
			if evaluationProxy == nil {
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "Evaluation Space is unavailable", 503)
				return
			}
			evaluationProxy.ServeHTTP(w, r)
		} else {
			appProxy.ServeHTTP(w, r)
		}
	}), nil
}

func cookieAllowed(name, prefix string) bool {
	if prefix != "" {
		app := appidentity.Application{Prefix: prefix}
		return name == app.SessionCookie() || name == app.LoginCookie()
	}
	return name == "project_login"
}

func filterCookies(r *http.Request, prefix string) error {
	kept := []*http.Cookie{}
	seen := map[string]bool{}
	for _, cookie := range r.Cookies() {
		if !cookieAllowed(cookie.Name, prefix) {
			continue
		}
		if seen[cookie.Name] {
			return errors.New("duplicate cookie")
		}
		seen[cookie.Name] = true
		kept = append(kept, cookie)
	}
	r.Header.Del("Cookie")
	for _, cookie := range kept {
		r.AddCookie(cookie)
	}
	return nil
}

func proxy(target, public *url.URL, transport http.RoundTripper, prefix string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport, FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = public.Host
			for key := range p.Out.Header {
				lower := strings.ToLower(key)
				if lower == "forwarded" || lower == "authorization" || lower == "proxy-authorization" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-project-") || strings.HasPrefix(lower, "x-studio-") || strings.HasPrefix(lower, "x-evaluation-") || strings.HasPrefix(lower, "x-identity-") || strings.HasPrefix(lower, "x-auth-") || lower == "remote-user" {
					p.Out.Header.Del(key)
				}
			}
			p.Out.Header.Set("X-Forwarded-Host", public.Host)
			p.Out.Header.Set("X-Forwarded-Proto", public.Scheme)
			if ip, _, err := net.SplitHostPort(p.In.RemoteAddr); err == nil {
				p.Out.Header.Set("X-Real-IP", ip)
				p.Out.Header.Set("X-Forwarded-For", ip)
			}
		},
		ModifyResponse: func(r *http.Response) error {
			for _, raw := range r.Header.Values("Set-Cookie") {
				c, err := http.ParseSetCookie(raw)
				if err != nil || !cookieAllowed(c.Name, prefix) || c.Domain != "" {
					return errors.New("upstream attempted unregistered cookie")
				}
				if prefix != "" && (c.Path != prefix || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode) {
					return errors.New("invalid application cookie scope")
				}
				if prefix == "" && c.Path != "/" {
					return errors.New("invalid application cookie scope")
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"error":"upstream_unavailable"}`)
		},
	}
}
