package servicegateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
)

// authority stands in for the product store. Tokens map to a Session, and the
// Session's live turn is resolved at each call, exactly as the store resolves it.
type authority struct {
	mu       sync.Mutex
	grants   map[string]product.ServiceGrant
	turns    map[string]string
	queries  int
	requests int
	released chan struct{}
}

func (a *authority) AcquireService(ctx context.Context, audience, token string) (product.Attribution, func(), error) {
	held, err := a.AuthorizeService(ctx, audience, token)
	if err != nil {
		return held, nil, err
	}
	a.mu.Lock()
	a.requests++
	a.mu.Unlock()
	var once sync.Once
	return held, func() {
		once.Do(func() {
			a.mu.Lock()
			a.requests--
			a.mu.Unlock()
			select {
			case a.released <- struct{}{}:
			default:
			}
		})
	}, nil
}

func (a *authority) AuthorizeService(_ context.Context, audience, token string) (product.Attribution, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queries++
	g, ok := a.grants[token]
	if !ok || g.Audience != audience {
		return product.Attribution{}, errors.New("denied")
	}
	turn, live := a.turns[g.SessionID]
	if !live {
		return product.Attribution{}, errors.New("no admitted turn")
	}
	return product.Attribution{Grant: g, TurnID: turn, RunState: "running"}, nil
}
func (a *authority) HoldsAttribution(_ context.Context, held product.Attribution) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.grants[held.Grant.ID]; !ok {
		return errors.New("revoked")
	}
	// A request holds only while its own turn is still the admitted one. A
	// successor turn on the same Session does not extend it.
	if a.turns[held.Grant.SessionID] != held.TurnID {
		return errors.New("turn completed")
	}
	return nil
}
func newAuthority() *authority {
	return &authority{
		grants: map[string]product.ServiceGrant{
			"a": {ID: "a", ProjectID: "p", SessionID: "a", IncarnationID: "ia", Audience: "model"},
			"b": {ID: "b", ProjectID: "p", SessionID: "b", IncarnationID: "ib", Audience: "model"},
		},
		turns: map[string]string{"a": "turn-a", "b": "turn-b"},
	}
}

func TestConcurrentRequestsAreAttributedAndDrainOnlyTheirOwnTurn(t *testing.T) {
	a := newAuthority()
	a.released = make(chan struct{}, 2)
	arrived := make(chan string, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Project-Id") != "p" || r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("Cookie") != "" {
			t.Error("upstream credential or attribution wrong")
		}
		turn := r.Header.Get("X-Turn-Id")
		if r.Header.Get("X-Session-Id") != strings.TrimPrefix(turn, "turn-") || r.Header.Get("X-Incarnation-Id") != "i"+strings.TrimPrefix(turn, "turn-") {
			t.Error("attribution is not host-derived", r.Header)
		}
		arrived <- turn
		select {
		case <-release:
			io.WriteString(w, turn)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	g, err := New(a, Config{Audience: "model", Upstream: upstream.URL, APIKey: "upstream-secret", MaxRequests: 2, RecheckInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	server := httptest.NewServer(g)
	defer server.Close()
	type result struct {
		token, body string
		status      int
		err         error
	}
	results := make(chan result, 2)
	for _, token := range []string{"a", "b"} {
		go func(token string) {
			req, _ := http.NewRequest("POST", server.URL, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token)
			// A guest cannot forge its own attribution.
			req.Header.Set("X-Turn-Id", "forged")
			req.Header.Set("X-Session-Id", "forged")
			req.Header.Set("X-Incarnation-Id", "forged")
			req.Header.Set("Cookie", "bad")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{token: token, err: err}
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			results <- result{token: token, body: string(body), status: resp.StatusCode}
		}(token)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent upstream requests missing")
		}
	}
	// Session A's turn completes. Only A's in-flight request drains; A's grant
	// survives for its next turn and B is untouched.
	a.mu.Lock()
	if a.requests != 2 {
		t.Errorf("request leases not retained: %d", a.requests)
	}
	a.turns["a"] = "turn-a-next"
	a.mu.Unlock()
	select {
	case r := <-results:
		if r.token != "a" || r.status != http.StatusBadGateway {
			t.Fatal(r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed turn's request was not drained")
	}
	close(release)
	select {
	case r := <-results:
		if r.token != "b" || r.status != 200 || r.body != "turn-b" {
			t.Fatal(r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sibling Session's request stalled")
	}
	for n := 0; n < 2; n++ {
		select {
		case <-a.released:
		case <-time.After(time.Second):
			t.Fatal("request handler did not release lease")
		}
	}
	a.mu.Lock()
	remaining := a.requests
	a.mu.Unlock()
	if remaining != 0 {
		t.Fatal("request leases not released", remaining)
	}
}

func TestGatewayDeniesAndReauthorizesEveryRequest(t *testing.T) {
	a := newAuthority()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("X-Turn-Id"))
	}))
	defer upstream.Close()
	g, err := New(a, Config{Audience: "model", Upstream: upstream.URL, APIKey: "upstream-secret", MaxRequests: 4})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(g)
	defer server.Close()
	call := func(method, header, token string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL, strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set(header, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := call("POST", "Authorization", "Bearer a"); code != 200 || body != "turn-a" {
		t.Fatal(code, body)
	}
	// The gateway caches no decision: the store is re-asked for each request, so a
	// later turn is a new attribution rather than the previous one reused.
	a.mu.Lock()
	a.turns["a"] = "turn-a-next"
	a.mu.Unlock()
	if code, body := call("POST", "X-Api-Key", "a"); code != 200 || body != "turn-a-next" {
		t.Fatal(code, body)
	}
	// With no admitted turn there is no authority, even though the grant is live.
	a.mu.Lock()
	delete(a.turns, "a")
	a.mu.Unlock()
	if code, _ := call("POST", "Authorization", "Bearer a"); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	a.mu.Lock()
	a.turns["a"] = "turn-a"
	delete(a.grants, "a")
	a.mu.Unlock()
	if code, _ := call("POST", "Authorization", "Bearer a"); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code, _ := call("POST", "Authorization", "Bearer unknown"); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code, _ := call("POST", "Authorization", ""); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code, _ := call("GET", "Authorization", "Bearer b"); code != http.StatusMethodNotAllowed {
		t.Fatal(code)
	}
	// A restarted gateway process holds no authority of its own; the store does.
	g.Close()
	if code, _ := call("POST", "Authorization", "Bearer b"); code != http.StatusServiceUnavailable {
		t.Fatal(code)
	}
	replacement, err := New(a, Config{Audience: "model", Upstream: upstream.URL, APIKey: "upstream-secret", MaxRequests: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	restarted := httptest.NewServer(replacement)
	defer restarted.Close()
	req, _ := http.NewRequest("POST", restarted.URL, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "turn-b" {
		t.Fatal(resp.StatusCode, string(body))
	}
}

func TestGatewayRejectsInvalidConfigurationAndBoundsConcurrency(t *testing.T) {
	a := newAuthority()
	for _, config := range []Config{
		{Audience: "model", Upstream: "http://x", MaxRequests: 0},
		{Audience: "", Upstream: "http://x", MaxRequests: 1},
		{Audience: "model", Upstream: "ftp://x", MaxRequests: 1},
		{Audience: "model", Upstream: "http://user:pass@x", MaxRequests: 1},
		{Audience: "model", Upstream: "http://x?q=1", MaxRequests: 1},
		{Audience: "model", Upstream: "/relative", MaxRequests: 1},
	} {
		if _, err := New(a, config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
	// One slot, and the first request holds it until released. The probe below
	// therefore cannot reach the upstream at all, so it must be refused rather
	// than queued: a bounded gateway sheds load instead of buffering it.
	occupied := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		occupied <- struct{}{}
		<-release
	}))
	defer upstream.Close()
	g, err := New(a, Config{Audience: "model", Upstream: upstream.URL, MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	server := httptest.NewServer(g)
	defer server.Close()
	holder := make(chan struct{})
	go func() {
		defer close(holder)
		req, _ := http.NewRequest("POST", server.URL, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer a")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-occupied:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("first request never reached the upstream")
	}
	req, _ := http.NewRequest("POST", server.URL, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	resp.Body.Close()
	close(release)
	<-holder
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("concurrency bound was not enforced: %d", resp.StatusCode)
	}
}

func TestPlatformNamespaceNeverFallsThroughWithoutCapability(t *testing.T) {
	forwarded := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++; w.WriteHeader(200) }))
	defer upstream.Close()
	gateway, err := New(newAuthority(), Config{Audience: "model", Upstream: upstream.URL, APIKey: "private-provider-key", MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	for _, path := range []string{"/platform", "/platform/", "/platform/v1/schedules", "/platform/v2/schedules", "/platform/v1/schedules/"} {
		request := httptest.NewRequest("POST", path, strings.NewReader(`{"prompt":"private task"}`))
		request.Header.Set("Authorization", "Bearer a")
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if response.Code != 503 || strings.Contains(response.Body.String(), "private") {
			t.Fatal(path, response.Code, response.Body.String())
		}
	}
	if forwarded != 0 {
		t.Fatal("platform payload forwarded", forwarded)
	}
}
