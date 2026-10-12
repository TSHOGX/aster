// Package edge is the single browser origin. It routes the product API, its
// projection stream and terminal sockets to the Go application, and everything
// else to the frontend.
//
// One Go hop preserves browser Origin and carries HTTP, SSE and WebSocket
// through the same authentication and authorization boundary.
package edge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Config names the two upstreams. Both are deployment configuration; neither is
// ever derived from a request.
type Config struct {
	// FrontDoor fixes public HTTPS authority behind an independent trusted proxy.
	FrontDoor *FrontDoor
	// Application serves the product API, events and terminal sockets.
	Application *url.URL
	// ApplicationSocket confines the local API hop to an owner-only Unix socket.
	ApplicationSocket string
	// Frontend serves assets and server-rendered pages.
	Frontend *url.URL
	// AdminFrontend is the independent application built with /admin basePath.
	AdminFrontend *url.URL
	// PageAccount supplies a verified account ID for first-paint appearance only.
	// Product APIs continue to resolve authority from the session cookie.
	PageAccount func(*http.Request) (string, error)
	// APIPrefixes are routed to Application. Everything else goes to Frontend.
	APIPrefixes []string
	// ResponseHeaderTimeout bounds an upstream's time to first byte. It must not
	// bound the body, because the event stream and terminal sockets are long-lived.
	ResponseHeaderTimeout time.Duration
}

const PageAccountHeader = "X-Project-Account-ID"

func (c Config) prefixes() []string {
	if len(c.APIPrefixes) > 0 {
		return c.APIPrefixes
	}
	return []string{"/api/"}
}

// New builds the edge handler. It fails rather than starting with an upstream it
// cannot address: a half-configured origin would serve the frontend for API
// paths and look like a mysterious 404 in the browser.
func New(config Config) (http.Handler, error) {
	if err := config.FrontDoor.Validate(); err != nil {
		return nil, err
	}
	if config.Application == nil || config.Frontend == nil || config.AdminFrontend == nil {
		return nil, errors.New("edge requires application, workbench and admin frontend upstreams")
	}
	for _, upstream := range []*url.URL{config.Application, config.Frontend, config.AdminFrontend} {
		if upstream.Scheme != "http" && upstream.Scheme != "https" {
			return nil, fmt.Errorf("edge upstream %q needs an http or https scheme", upstream)
		}
		if upstream.Host == "" {
			return nil, fmt.Errorf("edge upstream %q needs a host", upstream)
		}
	}
	timeout := config.ResponseHeaderTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: timeout,
		// Compression is left to the upstream: the event stream and terminal frames
		// must not be buffered by a decompressor on the way through.
		DisableCompression: true,
	}
	applicationTransport := transport
	if config.ApplicationSocket != "" {
		applicationTransport = transport.Clone()
		applicationTransport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", config.ApplicationSocket)
		}
	}
	application := reverseProxy(config.Application, applicationTransport, config.FrontDoor)
	frontend := reverseProxy(config.Frontend, transport, config.FrontDoor)
	admin := reverseProxy(config.AdminFrontend, transport, config.FrontDoor)
	prefixes := config.prefixes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !config.FrontDoor.accepts(r) {
			http.Error(w, "untrusted front door request", http.StatusForbidden)
			return
		}
		r.Header.Del(PageAccountHeader)
		for _, prefix := range prefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				application.ServeHTTP(w, r)
				return
			}
		}
		adminPath := r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/")
		page := r.URL.Path == "/" || adminPath && !strings.HasPrefix(r.URL.Path, "/admin/_next") && r.URL.Path != "/admin/favicon.svg"
		if page && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Cache-Control", "private, no-store")
			if config.PageAccount != nil {
				if account, err := config.PageAccount(r); err == nil && account != "" {
					r.Header.Set(PageAccountHeader, account)
				}
			}
		}
		if adminPath {
			admin.ServeHTTP(w, r)
			return
		}
		frontend.ServeHTTP(w, r)
	}), nil
}

func reverseProxy(upstream *url.URL, transport http.RoundTripper, frontDoor *FrontDoor) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,
		// FlushInterval -1 flushes every write immediately. Without it the event
		// stream and terminal output would sit in a buffer until it filled, which
		// looks exactly like a hung terminal.
		FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme = upstream.Scheme
			p.Out.URL.Host = upstream.Host
			p.Out.Host = p.In.Host
			// SetXForwarded replaces any inbound chain with the observed peer, so a
			// client-supplied X-Forwarded-For cannot become the leftmost entry, and
			// X-Forwarded-Host names the public origin rather than this hop. The
			// application is the origin authority, so Origin is passed through
			// untouched for the application's origin check.
			p.SetXForwarded()
			if host, _, err := net.SplitHostPort(p.In.RemoteAddr); err == nil {
				p.Out.Header.Set("X-Real-IP", host)
			}
			if frontDoor != nil {
				public, _ := url.Parse(frontDoor.PublicOrigin)
				p.Out.Host = public.Host
				p.Out.Header.Set("X-Forwarded-Host", public.Host)
				p.Out.Header.Set("X-Forwarded-Proto", public.Scheme)
				p.Out.Header.Set("X-Forwarded-For", p.In.Header.Get("X-Real-IP"))
				p.Out.Header.Set("X-Real-IP", p.In.Header.Get("X-Real-IP"))
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"upstream unavailable"}`)
		},
	}
}
