// Package gateway provides network admission and optional host-side adapters.
// Service credentials and resource restrictions belong to the adapters.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/egress"

	"github.com/google/uuid"
)

// ModelUpstream is one authorized destination for model generation. Credentials
// belong to this record, in the gateway process, and are attached to the upstream
// request only after the application authorized the guest's own request.
type ModelUpstream struct {
	// EndpointID selects the opaque managed endpoint this proxy serves.
	// Models is this proxy's own resource allowlist, never application policy.
	EndpointID string
	Models     []string
	BaseURL    string
	// APIKeyHeader and APIKey carry the upstream credential. A guest-supplied
	// authorization header is always discarded rather than forwarded.
	APIKeyHeader, APIKey string
	// ExtraHeaders are fixed upstream headers, such as an API version.
	ExtraHeaders map[string]string
}

// ModelOptions configures the model HTTP adapter.
type ModelOptions struct {
	BackendID string
	// Reserved is the synthetic destination range. A connection that did not
	// arrive through an assigned service route is refused before authorization.
	Reserved   netip.Prefix
	Upstreams  []ModelUpstream
	Flows      *Flows
	Authorizer ModelAuthorizer
	// RequestTimeout bounds one upstream generation. It is separate from the grant
	// deadline, which is the product's authority clock.
	RequestTimeout time.Duration
	// Client reaches the upstream. A deployment supplies one bound to the gateway
	// UID's egress path; the adapter never uses proxy environment variables.
	Client *http.Client
}

// ModelAuthorizer is the application's trusted authorization contract, reached
// over the protected Unix socket. Every request is authorized, including one
// reusing an established connection.
type ModelAuthorizer interface {
	Resolve(context.Context, egress.RouteRequest) (egress.RouteIdentity, error)
	Authorize(context.Context, egress.AuthorizationRequest) (egress.Authorization, error)
	Finish(context.Context, egress.RequestFinish) error
}

// ModelGateway enforces its own model allowlist after application route admission.
type ModelGateway struct {
	options   ModelOptions
	upstreams map[string]ModelUpstream
	mu        sync.Mutex
}

func NewModelGateway(p ModelOptions) (*ModelGateway, error) {
	if p.BackendID == "" || p.Flows == nil || p.Authorizer == nil || p.RequestTimeout <= 0 {
		return nil, fmt.Errorf("model gateway backend identity, flow registry, authorizer and timeout required")
	}
	if !p.Reserved.IsValid() || !p.Reserved.Addr().Is4() || !p.Reserved.Addr().IsPrivate() {
		return nil, fmt.Errorf("model gateway requires the private synthetic service range")
	}
	if len(p.Upstreams) == 0 {
		return nil, fmt.Errorf("at least one model upstream required")
	}
	upstreams := map[string]ModelUpstream{}
	for _, upstream := range p.Upstreams {
		if upstream.EndpointID == "" || upstream.BaseURL == "" || len(upstream.Models) == 0 {
			return nil, fmt.Errorf("model upstream reference and base URL required")
		}
		endpoint, err := url.Parse(upstream.BaseURL)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return nil, fmt.Errorf("model upstream %q must be an origin without credentials or query", upstream.EndpointID)
		}
		if (upstream.APIKey == "") != (upstream.APIKeyHeader == "") {
			return nil, fmt.Errorf("model upstream %q credential header and value must be given together", upstream.EndpointID)
		}
		if _, exists := upstreams[upstream.EndpointID]; exists {
			return nil, fmt.Errorf("duplicate model upstream %q", upstream.EndpointID)
		}
		upstreams[upstream.EndpointID] = upstream
	}
	if p.Client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		p.Client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &ModelGateway{options: p, upstreams: upstreams}, nil
}

// maxModelRequestBytes bounds one generation request body. The adapter must read
// the body to learn which model is being requested, so it cannot be unbounded.
const maxModelRequestBytes = 8 << 20

// ServeHTTP authorizes and forwards one model generation.
//
// The destination comes from the kernel, never from a guest header: a guest that
// sets Host or Authorization cannot select a service or supply a credential.
func (g *ModelGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	destination, ok := r.Context().Value(originalDestinationKey{}).(netip.AddrPort)
	if !ok || !g.options.Reserved.Contains(destination.Addr()) {
		http.Error(w, "connection did not arrive through an assigned service route", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" && r.URL.RawQuery == "" {
		ctx, cancel := context.WithTimeout(r.Context(), min(g.options.RequestTimeout, 5*time.Second))
		defer cancel()
		route, err := g.options.Authorizer.Resolve(ctx, egress.RouteRequest{BackendID: g.options.BackendID, Destination: destination})
		if err != nil {
			slog.Warn("route readiness denied", "destination", destination.String(), "error", err)
			writeAuthorizationFailure(w, err)
			return
		}
		if _, exists := g.upstreams[route.EndpointID]; !exists || !g.options.Flows.Ready(route) {
			http.Error(w, "route is not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ready":true}`)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "only generation requests are supported", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxModelRequestBytes))
	if err != nil {
		http.Error(w, "generation request exceeds bound", http.StatusRequestEntityTooLarge)
		return
	}
	// Resource restrictions belong to this adapter; the application only sees
	// the destination and request identity.
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.Model) == "" {
		http.Error(w, "generation request must name a model", http.StatusBadRequest)
		return
	}
	// The kernel destination selects immutable application route authority.
	request := egress.AuthorizationRequest{
		BackendID: g.options.BackendID, Destination: destination,
		RequestID: "model-" + uuid.NewString(),
	}
	authorization, err := g.authorize(r.Context(), request)
	if err != nil {
		writeAuthorizationFailure(w, err)
		return
	}
	upstream, known := g.upstreams[authorization.EndpointID]
	if !known {
		// A service the product authorized but this gateway holds no credentials
		// for is a deployment mismatch, not something to improvise around.
		g.finish(r.Context(), request.RequestID, "upstream_unconfigured", 0)
		http.Error(w, "no upstream is configured for the authorized service", http.StatusServiceUnavailable)
		return
	}
	if !modelAllowed(upstream.Models, payload.Model) {
		g.finish(r.Context(), request.RequestID, "model_denied", 0)
		http.Error(w, "model is not allowed by this proxy", http.StatusForbidden)
		return
	}

	flowCtx, flow, err := g.options.Flows.Begin(r.Context(), authorization)
	if err != nil {
		g.finish(r.Context(), request.RequestID, "admission_revoked", 0)
		writeAuthorizationFailure(w, err)
		return
	}
	// Finish runs after upstream activity has stopped, which is what makes a
	// revocation acknowledgment mean something.
	defer flow.Finish()
	forwarded, outcome, err := g.forward(flowCtx, upstream, r, body, w)
	g.finish(r.Context(), request.RequestID, outcome, forwarded)
	if err != nil && forwarded == 0 {
		writeUpstreamFailure(w, err)
	}
}

func (g *ModelGateway) authorize(ctx context.Context, request egress.AuthorizationRequest) (egress.Authorization, error) {
	ctx, cancel := context.WithTimeout(ctx, g.options.RequestTimeout)
	defer cancel()
	return g.options.Authorizer.Authorize(ctx, request)
}

// forward relays one generation to the upstream and streams its response back.
// The guest's own authorization, cookie and proxy headers are dropped: only this
// gateway's credential may reach the upstream.
func (g *ModelGateway) forward(ctx context.Context, upstream ModelUpstream, original *http.Request, body []byte, w http.ResponseWriter) (int64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.options.RequestTimeout)
	defer cancel()
	target := strings.TrimRight(upstream.BaseURL, "/") + original.URL.Path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, "invalid_upstream_request", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", original.Header.Get("Accept"))
	for name, value := range upstream.ExtraHeaders {
		request.Header.Set(name, value)
	}
	if upstream.APIKeyHeader != "" {
		request.Header.Set(upstream.APIKeyHeader, upstream.APIKey)
	}
	response, err := g.options.Client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, "upstream_timeout", err
		}
		return 0, "upstream_unavailable", err
	}
	defer response.Body.Close()
	for _, name := range []string{"Content-Type", "Cache-Control"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	flusher, streaming := w.(http.Flusher)
	var forwarded int64
	buffer := make([]byte, 16<<10)
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			written, writeErr := w.Write(buffer[:read])
			forwarded += int64(written)
			if streaming {
				flusher.Flush()
			}
			if writeErr != nil {
				return forwarded, "client_gone", writeErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return forwarded, "upstream_interrupted", readErr
		}
	}
	outcome := "ok"
	if response.StatusCode/100 != 2 {
		outcome = fmt.Sprintf("upstream_%d", response.StatusCode)
	}
	return forwarded, outcome, nil
}

// finish reports the settled request. It uses a context detached from the guest
// connection: a client that hung up cannot prevent the application from learning
// that this request is over.
func (g *ModelGateway) finish(ctx context.Context, request, outcome string, bytes int64) {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.options.RequestTimeout)
	defer cancel()
	_ = g.options.Authorizer.Finish(settle, egress.RequestFinish{RequestID: request, BackendID: g.options.BackendID, Outcome: outcome, Bytes: bytes})
}

func writeAuthorizationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, egress.ErrDenied), errors.Is(err, ErrRevoked):
		http.Error(w, "request is not authorized", http.StatusForbidden)
	case errors.Is(err, egress.ErrRequestConflict):
		http.Error(w, "request identity conflict", http.StatusConflict)
	default:
		http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
	}
}

func writeUpstreamFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "upstream did not answer in time", http.StatusGatewayTimeout)
		return
	}
	http.Error(w, "upstream unavailable", http.StatusBadGateway)
}

type originalDestinationKey struct{}

// DestinationContext attaches the kernel's pre-NAT destination to an accepted
// connection's context. It is a net/http ConnContext, so a handler can only
// obtain a destination the kernel reported for that socket.
func DestinationContext(reserved netip.Prefix) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, connection net.Conn) context.Context {
		tcp, ok := connection.(*net.TCPConn)
		if !ok {
			return ctx
		}
		destination, err := AcceptDestination(tcp, reserved)
		if err != nil {
			slog.Warn("gateway ingress refused", "local", connection.LocalAddr(), "error", err)
			return ctx
		}
		return context.WithValue(ctx, originalDestinationKey{}, destination)
	}
}

func modelAllowed(models []string, requested string) bool {
	for _, model := range models {
		if model == requested {
			return true
		}
	}
	return false
}
