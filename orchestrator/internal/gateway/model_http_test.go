package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

// scriptedAuthorizer stands in for the application's trusted contract. It records
// every request, so a test can check that the adapter authorizes each one and
// derives the resource from the actual body.
type scriptedAuthorizer struct {
	mu        sync.Mutex
	requests  []egress.AuthorizationRequest
	finishes  []egress.RequestFinish
	deny      bool
	fail      error
	service   egress.Endpoint
	expires   time.Duration
	bindingID string
}

func (a *scriptedAuthorizer) Authorize(_ context.Context, request egress.AuthorizationRequest) (egress.Authorization, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.mu.Unlock()
	if a.fail != nil {
		return egress.Authorization{}, a.fail
	}
	if a.deny {
		return egress.Authorization{}, egress.ErrDenied
	}
	expires := a.expires
	if expires == 0 {
		expires = time.Minute
	}
	binding := a.bindingID
	if binding == "" {
		binding = "route_model"
	}
	return egress.Authorization{
		RequestContext: egress.RequestContext{
			SessionID: "session", UserID: "user", RequestID: request.RequestID, GrantID: "grant",
			BindingID: binding, BackendID: request.BackendID, GenerationID: "generation",
			EndpointID: a.service.ID, TurnID: 7, Epoch: 1, BindingRevision: 1,
		},
		PolicyDigest: "digest", ExpiresAt: time.Now().Add(expires),
	}, nil
}

func (a *scriptedAuthorizer) Finish(_ context.Context, request egress.RequestFinish) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finishes = append(a.finishes, request)
	return nil
}

func (a *scriptedAuthorizer) observed() ([]egress.AuthorizationRequest, []egress.RequestFinish) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]egress.AuthorizationRequest(nil), a.requests...), append([]egress.RequestFinish(nil), a.finishes...)
}

type upstreamRecord struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []string
}

func (u *upstreamRecord) seen() ([]http.Header, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]http.Header(nil), u.headers...), append([]string(nil), u.bodies...)
}

func modelFixture(t *testing.T, handler http.HandlerFunc) (*ModelGateway, *scriptedAuthorizer, *Flows, *upstreamRecord) {
	t.Helper()
	record := &upstreamRecord{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		record.mu.Lock()
		record.headers = append(record.headers, r.Header.Clone())
		record.bodies = append(record.bodies, string(body))
		record.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)
	authorizer := &scriptedAuthorizer{service: egress.Endpoint{ID: "model", NextHop: "model", Transport: "tcp", Hostname: "model.internal", Port: 443}}
	flows := NewFlows()
	if err := flows.Activate("route_model", 1); err != nil {
		t.Fatal(err)
	}
	gateway, err := NewModelGateway(ModelOptions{
		BackendID: "engine-store", Reserved: netip.MustParsePrefix("10.80.0.0/24"),
		Upstreams:  []ModelUpstream{{EndpointID: "model", Models: []string{"approved-model"}, BaseURL: upstream.URL, APIKeyHeader: "X-Api-Key", APIKey: "upstream-secret", ExtraHeaders: map[string]string{"Anthropic-Version": "2023-06-01"}}},
		Flows:      flows,
		Authorizer: authorizer, RequestTimeout: 5 * time.Second, Client: upstream.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway, authorizer, flows, record
}

func steered(t *testing.T, gateway *ModelGateway, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), originalDestinationKey{}, netip.MustParseAddrPort("10.80.0.1:443")))
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	return response
}

func TestModelGatewayForwardsAuthorizedGenerationWithoutLeakingCredentials(t *testing.T) {
	gateway, authorizer, _, record := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"generated"}]}`))
	})
	response := steered(t, gateway, `{"model":"approved-model","messages":[]}`, func(r *http.Request) {
		// A guest that supplies its own credential or selects a host must not have
		// either reach the upstream.
		r.Header.Set("Authorization", "Bearer guest-token")
		r.Header.Set("X-Api-Key", "guest-key")
		r.Host = "attacker.example"
	})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "generated") {
		t.Fatalf("authorized generation: %d %s", response.Code, response.Body.String())
	}
	headers, bodies := record.seen()
	if len(headers) != 1 {
		t.Fatalf("expected one upstream request: %d", len(headers))
	}
	if headers[0].Get("X-Api-Key") != "upstream-secret" {
		t.Fatalf("upstream credential was not the gateway's: %q", headers[0].Get("X-Api-Key"))
	}
	if headers[0].Get("Authorization") != "" {
		t.Fatal("guest authorization header was forwarded upstream")
	}
	if headers[0].Get("Anthropic-Version") != "2023-06-01" {
		t.Fatal("configured upstream header missing")
	}
	if bodies[0] != `{"model":"approved-model","messages":[]}` {
		t.Fatalf("upstream body was rewritten: %s", bodies[0])
	}
	requests, finishes := authorizer.observed()
	if len(requests) != 1 || requests[0].Destination != netip.MustParseAddrPort("10.80.0.1:443") {
		t.Fatalf("authorization did not describe the actual request: %+v", requests)
	}
	if requests[0].Destination.String() != "10.80.0.1:443" {
		t.Fatalf("authorization used a destination other than the kernel's: %+v", requests[0])
	}
	if requests[0].EndpointID != "" {
		t.Fatal("adapter asserted a service identity it cannot derive")
	}
	if len(finishes) != 1 || finishes[0].Outcome != "ok" || finishes[0].Bytes == 0 {
		t.Fatalf("request was not settled: %+v", finishes)
	}
}

func TestModelGatewayAuthorizesEveryRequestOnAReusedConnection(t *testing.T) {
	gateway, authorizer, _, _ := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	for range 3 {
		if response := steered(t, gateway, `{"model":"approved-model"}`, nil); response.Code != http.StatusOK {
			t.Fatalf("generation: %d", response.Code)
		}
	}
	requests, finishes := authorizer.observed()
	if len(requests) != 3 || len(finishes) != 3 {
		t.Fatalf("each request must be authorized and settled: %d %d", len(requests), len(finishes))
	}
	seen := map[string]bool{}
	for _, request := range requests {
		if seen[request.RequestID] {
			t.Fatal("request identities were reused across generations")
		}
		seen[request.RequestID] = true
	}
}

func TestModelGatewayRefusesUnsteeredAndUnauthorizedRequests(t *testing.T) {
	gateway, authorizer, _, record := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	// A connection that did not arrive through an assigned route has no service.
	direct := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"approved-model"}`))
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, direct)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unsteered connection: %d", response.Code)
	}
	// A destination outside the reserved range is equally not a service.
	outside := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"approved-model"}`))
	outside = outside.WithContext(context.WithValue(outside.Context(), originalDestinationKey{}, netip.MustParseAddrPort("192.0.2.9:443")))
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, outside)
	if response.Code != http.StatusForbidden {
		t.Fatalf("destination outside the reserved range: %d", response.Code)
	}
	if response := steered(t, gateway, `{"messages":[]}`, nil); response.Code != http.StatusBadRequest {
		t.Fatalf("request naming no model: %d", response.Code)
	}
	if response := steered(t, gateway, `not json`, nil); response.Code != http.StatusBadRequest {
		t.Fatalf("unparsable request: %d", response.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	get = get.WithContext(context.WithValue(get.Context(), originalDestinationKey{}, netip.MustParseAddrPort("10.80.0.1:443")))
	response = httptest.NewRecorder()
	gateway.ServeHTTP(response, get)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("non-generation method: %d", response.Code)
	}
	if headers, _ := record.seen(); len(headers) != 0 {
		t.Fatal("a refused request reached the upstream")
	}
	// A denied model never reaches the upstream either.
	authorizer.deny = true
	if response := steered(t, gateway, `{"model":"unapproved-model"}`, nil); response.Code != http.StatusForbidden {
		t.Fatalf("denied model: %d", response.Code)
	}
	if headers, _ := record.seen(); len(headers) != 0 {
		t.Fatal("a denied request reached the upstream")
	}
	authorizer.deny = false
	authorizer.fail = fmt.Errorf("authorization socket unavailable")
	if response := steered(t, gateway, `{"model":"approved-model"}`, nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable authorization must fail closed: %d", response.Code)
	}
	if headers, _ := record.seen(); len(headers) != 0 {
		t.Fatal("a request reached the upstream while authorization was unavailable")
	}
}

func TestModelGatewayRefusesServiceWithoutConfiguredUpstream(t *testing.T) {
	gateway, authorizer, _, record := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	service := authorizer.service
	service.ID = "unconfigured"
	authorizer.service = service
	if response := steered(t, gateway, `{"model":"approved-model"}`, nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("service with no credentials here: %d", response.Code)
	}
	if headers, _ := record.seen(); len(headers) != 0 {
		t.Fatal("a request without a configured upstream was forwarded")
	}
}

// Revoking the grant must stop an in-flight stream and be acknowledged only after
// the upstream work has actually stopped.
func TestModelGatewayRevocationStopsAnInFlightStream(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	gateway, _, flows, _ := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	done := make(chan int, 1)
	go func() {
		response := steered(t, gateway, `{"model":"approved-model","stream":true}`, nil)
		done <- response.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream stream never started")
	}
	revoked := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		revoked <- flows.RevokeGrant(ctx, "grant")
	}()
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatalf("grant revocation did not settle: %v", err)
		}
	case <-time.After(6 * time.Second):
		close(release)
		t.Fatal("grant revocation never acknowledged")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("revoked request did not return")
	}
	// The tombstoned grant cannot be used again.
	if response := steered(t, gateway, `{"model":"approved-model"}`, nil); response.Code != http.StatusForbidden {
		t.Fatalf("a revoked grant was reused: %d", response.Code)
	}
}

func TestModelGatewayReportsUpstreamFailureOutcomes(t *testing.T) {
	gateway, authorizer, _, _ := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	})
	response := steered(t, gateway, `{"model":"approved-model"}`, nil)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("upstream status was not passed through: %d", response.Code)
	}
	_, finishes := authorizer.observed()
	if len(finishes) != 1 || finishes[0].Outcome != "upstream_429" {
		t.Fatalf("upstream failure was not recorded: %+v", finishes)
	}
}

func TestModelGatewayRequiresCompleteConfiguration(t *testing.T) {
	valid := ModelOptions{
		BackendID: "engine", Reserved: netip.MustParsePrefix("10.80.0.0/24"),
		Upstreams: []ModelUpstream{{EndpointID: "model", Models: []string{"approved-model"}, BaseURL: "https://upstream.example"}},
		Flows:     NewFlows(), Authorizer: &scriptedAuthorizer{}, RequestTimeout: time.Second,
	}
	if _, err := NewModelGateway(valid); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ModelOptions){
		"no backend":         func(o *ModelOptions) { o.BackendID = "" },
		"public range":       func(o *ModelOptions) { o.Reserved = netip.MustParsePrefix("8.8.8.0/24") },
		"invalid range":      func(o *ModelOptions) { o.Reserved = netip.Prefix{} },
		"no upstream":        func(o *ModelOptions) { o.Upstreams = nil },
		"no flows":           func(o *ModelOptions) { o.Flows = nil },
		"no authorizer":      func(o *ModelOptions) { o.Authorizer = nil },
		"no timeout":         func(o *ModelOptions) { o.RequestTimeout = 0 },
		"credential in url":  func(o *ModelOptions) { o.Upstreams[0].BaseURL = "https://user:pass@upstream.example" },
		"query in url":       func(o *ModelOptions) { o.Upstreams[0].BaseURL = "https://upstream.example?key=secret" },
		"half credential":    func(o *ModelOptions) { o.Upstreams[0].APIKey = "secret" },
		"duplicate upstream": func(o *ModelOptions) { o.Upstreams = append(o.Upstreams, o.Upstreams[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			options.Upstreams = append([]ModelUpstream(nil), valid.Upstreams...)
			mutate(&options)
			if _, err := NewModelGateway(options); err == nil {
				t.Fatalf("invalid configuration accepted: %s", name)
			}
		})
	}
}

func TestModelGatewayBoundsRequestBodies(t *testing.T) {
	gateway, _, _, record := modelFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	oversized, _ := json.Marshal(map[string]string{"model": "approved-model", "padding": strings.Repeat("x", maxModelRequestBytes)})
	if response := steered(t, gateway, string(oversized), nil); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", response.Code)
	}
	if headers, _ := record.seen(); len(headers) != 0 {
		t.Fatal("an oversized request reached the upstream")
	}
}

func TestModelAllowlistBelongsToProxyAfterGenericRouteAdmission(t *testing.T) {
	proxy, authorizer, _, upstream := modelFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("forbidden model reached upstream") })
	response := steered(t, proxy, `{"model":"forbidden"}`, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status %d", response.Code)
	}
	requests, finishes := authorizer.observed()
	if len(requests) != 1 || len(finishes) != 1 || finishes[0].Outcome != "model_denied" {
		t.Fatalf("route admission and local refusal: %+v %+v", requests, finishes)
	}
	raw, err := json.Marshal(requests[0])
	if err != nil || strings.Contains(string(raw), `"model"`) || strings.Contains(string(raw), `"operation"`) {
		t.Fatalf("resource policy leaked into network request: %s %v", raw, err)
	}
	if headers, _ := upstream.seen(); len(headers) != 0 {
		t.Fatal("denied model forwarded")
	}
}

func (a *scriptedAuthorizer) Resolve(_ context.Context, request egress.RouteRequest) (egress.RouteIdentity, error) {
	if a.fail != nil {
		return egress.RouteIdentity{}, a.fail
	}
	if request.Destination != netip.MustParseAddrPort("10.80.0.1:443") {
		return egress.RouteIdentity{}, egress.ErrDenied
	}
	binding := a.bindingID
	if binding == "" {
		binding = "route_model"
	}
	return egress.RouteIdentity{BindingID: binding, BindingRevision: 1, EndpointID: a.service.ID}, nil
}
func TestReadinessUsesActivatedBindingWithoutTurnGrantOrUpstream(t *testing.T) {
	proxy, authorizer, flows, upstream := modelFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("health probe forwarded") })
	authorizer.deny = true // No turn grant exists during startup.
	probe := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request = request.WithContext(context.WithValue(request.Context(), originalDestinationKey{}, netip.MustParseAddrPort("10.80.0.1:443")))
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		return response
	}
	if response := probe("/healthz"); response.Code != http.StatusOK || response.Body.String() != `{"ready":true}` {
		t.Fatalf("readiness: %d %s", response.Code, response.Body)
	}
	if response := probe("/healthz?forward=1"); response.Code == http.StatusOK {
		t.Fatal("query bypass accepted")
	}
	if response := steered(t, proxy, `{"model":"approved-model"}`, nil); response.Code != http.StatusForbidden {
		t.Fatal("readiness granted workload access")
	}
	if err := flows.Revoke(context.Background(), "route_model"); err != nil {
		t.Fatal(err)
	}
	if response := probe("/healthz"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("revoked route readiness: %d", response.Code)
	}
	if headers, _ := upstream.seen(); len(headers) != 0 {
		t.Fatal("readiness made an upstream request")
	}
}
