// Package servicegateway terminates steered guest traffic for one declared
// Project service audience. It authorizes every request against the product
// store, attributes it to the Session's live turn, and holds the upstream
// credential the guest never sees.
//
// It is not a security boundary between sibling Project processes: guest root can
// read another Session's credential. It is request attribution plus upstream
// secret custody.
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

// Authority is the product store. The gateway never caches a decision: a
// revoked grant or a completed turn must stop the next request immediately.
type Authority interface {
	AuthorizeService(context.Context, string, string) (product.Attribution, error)
	HoldsAttribution(context.Context, product.Attribution) error
}
type Config struct {
	// Audience must match a Project profile service declaration. APIKey is the
	// upstream credential; it is configured here and never in product state.
	Audience, Upstream, APIKey string
	MaxRequests                int
	// RecheckInterval bounds how long an in-flight request may outlive its turn.
	RecheckInterval time.Duration
	Transport       http.RoundTripper
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
	if config.RecheckInterval <= 0 {
		config.RecheckInterval = 100 * time.Millisecond
	}
	return &Gateway{authority: authority, config: config, target: target, slots: make(chan struct{}, config.MaxRequests), active: map[uint64]context.CancelFunc{}}, nil
}

// Close drains this process. Authority itself is durable, so a restart loses no
// grant: the next request re-authorizes against the store.
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	attribution, err := g.authority.AuthorizeService(r.Context(), g.config.Audience, token)
	if err != nil {
		http.Error(w, "service authorization denied", http.StatusUnauthorized)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "service capacity exhausted", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		http.Error(w, "gateway unavailable", http.StatusServiceUnavailable)
		return
	}
	g.next++
	id := g.next
	g.active[id] = cancel
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.active, id); g.mu.Unlock() }()
	done := make(chan struct{})
	defer close(done)
	// This request is pinned to the turn it was attributed to. Completing that
	// turn drains only this request; a sibling Session's turn is untouched, and a
	// later turn on the same Session is a new attribution rather than an
	// inheritance of this one.
	go func() {
		ticker := time.NewTicker(g.config.RecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := g.authority.HoldsAttribution(ctx, attribution); err != nil {
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
		req.Header.Del("X-Session-Id")
		req.Header.Del("X-Turn-Id")
		req.Header.Del("X-Incarnation-Id")
		if g.config.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+g.config.APIKey)
			req.Header.Set("X-Api-Key", g.config.APIKey)
		}
		// Only host-derived attribution reaches the upstream, never guest claims.
		req.Header.Set("X-Project-Id", attribution.Grant.ProjectID)
		req.Header.Set("X-Session-Id", attribution.Grant.SessionID)
		req.Header.Set("X-Incarnation-Id", attribution.Grant.IncarnationID)
		req.Header.Set("X-Turn-Id", attribution.TurnID)
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "service request failed", http.StatusBadGateway)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
