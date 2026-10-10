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
	"sync/atomic"
	"time"

	"harness-platform/orchestrator/internal/product"
)

// Authority resolves each new request against the owner. Existing requests
// may bridge only a bounded unavailable interval; confirmed denial ends them.
type Authority interface {
	AcquireService(context.Context, string, string) (product.Attribution, func(), error)
	HoldsAttribution(context.Context, product.Attribution) error
}
type Config struct {
	// Audience must match a Project profile service declaration. APIKey is the
	// upstream credential; it is configured here and never in product state.
	Audience, Upstream, APIKey string
	MaxRequests                int
	// RecheckInterval bounds how long an in-flight request may outlive its turn.
	RecheckInterval time.Duration
	// AuthorityGrace bounds continuation since the last successful check began.
	// Zero selects five seconds; deployments cannot increase this ceiling.
	AuthorityGrace time.Duration
	Transport      http.RoundTripper
	Platform       Platform
}
type Gateway struct {
	authority                                            Authority
	config                                               Config
	target                                               *url.URL
	slots                                                chan struct{}
	mu                                                   sync.Mutex
	active                                               map[uint64]context.CancelFunc
	next                                                 uint64
	closed                                               bool
	authorityFailures, authorityExpired, authorityDenied atomic.Uint64
	platformSuccess, platformUnknown                     atomic.Uint64
}

func New(authority Authority, config Config) (*Gateway, error) {
	target, err := url.Parse(config.Upstream)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Scheme != "http" && target.Scheme != "https") || config.Audience == "" || config.MaxRequests < 1 {
		return nil, errors.New("invalid service gateway configuration")
	}
	if authority == nil || config.AuthorityGrace < 0 || config.AuthorityGrace > AuthorityGrace {
		return nil, errors.New("invalid authority continuation window")
	}
	if config.AuthorityGrace == 0 {
		config.AuthorityGrace = AuthorityGrace
	}
	if config.RecheckInterval <= 0 {
		config.RecheckInterval = 100 * time.Millisecond
	}
	return &Gateway{authority: authority, config: config, target: target, slots: make(chan struct{}, config.MaxRequests), active: map[uint64]context.CancelFunc{}}, nil
}

// Close cancels forwarding. A graceful gateway update first stops admission and
// waits for all handlers; cancellation is not a successful drain.
func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for _, cancel := range g.active {
		cancel()
	}
}
func (g *Gateway) AuthorityStats() map[string]uint64 {
	return map[string]uint64{"unavailable": g.authorityFailures.Load(), "expired": g.authorityExpired.Load(), "denied": g.authorityDenied.Load(), "platform_success": g.platformSuccess.Load(), "platform_outcome_unknown": g.platformUnknown.Load()}
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
	// Bound requests before any authority I/O.
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "service capacity exhausted", http.StatusTooManyRequests)
		return
	}
	authorizedAt := time.Now()
	attribution, release, err := g.authority.AcquireService(r.Context(), g.config.Audience, token)
	if err != nil {
		if errors.Is(err, product.ErrDraining) || errors.Is(err, ErrAuthorityUnavailable) {
			http.Error(w, "gateway release pending", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, product.ErrCapacity) {
			http.Error(w, "service capacity exhausted", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "service authorization denied", http.StatusUnauthorized)
		return
	}
	defer release()
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
		expires := authorizedAt.Add(g.config.AuthorityGrace)
		deadline := time.NewTimer(time.Until(expires))
		defer deadline.Stop()
		ticker := time.NewTicker(g.config.RecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-deadline.C:
				g.authorityExpired.Add(1)
				cancel()
				return
			case <-ticker.C:
				started := time.Now()
				checkCtx, checkCancel := context.WithDeadline(ctx, expires)
				err := g.authority.HoldsAttribution(checkCtx, attribution)
				checkCancel()
				if errors.Is(err, ErrAuthorityUnavailable) {
					g.authorityFailures.Add(1)
				}
				if !time.Now().Before(expires) || (err != nil && !errors.Is(err, ErrAuthorityUnavailable)) {
					if !time.Now().Before(expires) {
						g.authorityExpired.Add(1)
					} else {
						g.authorityDenied.Add(1)
					}
					cancel()
					return
				}
				if err == nil {
					// Count RPC latency inside the window, never after it.
					expires = started.Add(g.config.AuthorityGrace)
					if !deadline.Stop() {
						select {
						case <-deadline.C:
						default:
						}
					}
					deadline.Reset(time.Until(expires))
				}
			}
		}
	}()
	// Reserve the entire namespace, including unknown versions, before any
	// provider URL construction or credential injection.
	if r.URL.Path == "/platform" || strings.HasPrefix(r.URL.Path, "/platform/") {
		g.servePlatform(w, r.WithContext(ctx), attribution)
		return
	}
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
