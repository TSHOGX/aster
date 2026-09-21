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

type authority struct {
	mu     sync.Mutex
	grants map[string]product.ServiceGrant
}

func (a *authority) AuthorizeService(_ context.Context, audience, token string) (product.ServiceGrant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.grants[token]
	if !ok || g.Audience != audience {
		return g, errors.New("denied")
	}
	return g, nil
}
func TestConcurrentRequestsDrainOnlyRevokedTurn(t *testing.T) {
	a := &authority{grants: map[string]product.ServiceGrant{"a": {ProjectID: "p", SessionID: "a", TurnID: "turn-a", Audience: "model"}, "b": {ProjectID: "p", SessionID: "b", TurnID: "turn-b", Audience: "model"}}}
	arrived := make(chan string, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Project-Id") != "p" || r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("Cookie") != "" {
			t.Error("upstream auth/attribution")
		}
		turn := r.Header.Get("X-Turn-Id")
		arrived <- turn
		select {
		case <-release:
			io.WriteString(w, turn)
		case <-r.Context().Done():
			return
		}
	}))
	defer upstream.Close()
	g, err := New(a, Config{Audience: "model", Upstream: upstream.URL, APIKey: "upstream-secret", MaxRequests: 2})
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
			req.Header.Set("X-Turn-Id", "forged")
			req.Header.Set("Cookie", "bad")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{token: token, err: err}
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			results <- result{token: token, body: string(b), status: resp.StatusCode}
		}(token)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent upstream requests missing")
		}
	}
	a.mu.Lock()
	delete(a.grants, "a")
	a.mu.Unlock()
	select {
	case r := <-results:
		if r.token != "a" || r.status != 502 {
			t.Fatal(r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked request not drained")
	}
	close(release)
	select {
	case r := <-results:
		if r.token != "b" || r.status != 200 || r.body != "turn-b" {
			t.Fatal(r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sibling request stalled")
	}
	req, _ := http.NewRequest("POST", server.URL, strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
}
