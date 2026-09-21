// Package servicegateway authorizes individual service requests under the
// original Agent turn. Generic network reachability remains Project-scoped.
package servicegateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/product"
)

type Authority interface {
	AuthorizeService(context.Context, string, string) (product.ServiceGrant, error)
}
type Config struct {
	Audience, Upstream, APIKey string
	MaxRequests                int
	Transport                  http.RoundTripper
}
type Gateway struct {
	authority Authority
	config    Config
	target    *url.URL
	slots     chan struct{}
	mu        sync.Mutex
	active    map[uint64]context.CancelFunc
	next      uint64
	closed    bool
}

func New(authority Authority, config Config) (*Gateway, error) {
	target, err := url.Parse(config.Upstream)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Scheme != "http" && target.Scheme != "https") || config.Audience == "" || config.MaxRequests < 1 {
		return nil, errors.New("invalid service gateway configuration")
	}
	return &Gateway{authority: authority, config: config, target: target, slots: make(chan struct{}, config.MaxRequests), active: map[uint64]context.CancelFunc{}}, nil
}
func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for _, cancel := range g.active {
		cancel()
	}
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	grant, err := g.authority.AuthorizeService(r.Context(), g.config.Audience, token)
	if err != nil {
		http.Error(w, "service authorization denied", 401)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "service capacity exhausted", 429)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		http.Error(w, "gateway unavailable", 503)
		return
	}
	g.next++
	id := g.next
	g.active[id] = cancel
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.active, id); g.mu.Unlock() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := g.authority.AuthorizeService(ctx, g.config.Audience, token); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	proxy := httputil.NewSingleHostReverseProxy(g.target)
	proxy.Transport = g.config.Transport
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.Host = g.target.Host
		req.Header.Del("Authorization")
		req.Header.Del("X-Api-Key")
		req.Header.Del("Cookie")
		req.Header.Del("X-Project-Id")
		req.Header.Del("X-Turn-Id")
		req.Header.Del("X-Session-Id")
		if g.config.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+g.config.APIKey)
			req.Header.Set("X-Api-Key", g.config.APIKey)
		}
		// Only host-derived attribution reaches the upstream, never guest claims.
		req.Header.Set("X-Project-Id", grant.ProjectID)
		req.Header.Set("X-Session-Id", grant.SessionID)
		req.Header.Set("X-Turn-Id", grant.TurnID)
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "service request failed", 502) }
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
