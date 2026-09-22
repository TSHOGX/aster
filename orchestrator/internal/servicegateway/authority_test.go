package servicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
)

type interruptedAuthority struct {
	*authority
	mode atomic.Int32
}

func (a *interruptedAuthority) AcquireService(ctx context.Context, audience, token string) (product.Attribution, func(), error) {
	if a.mode.Load() != 0 {
		return product.Attribution{}, nil, ErrAuthorityUnavailable
	}
	return a.authority.AcquireService(ctx, audience, token)
}
func (a *interruptedAuthority) HoldsAttribution(ctx context.Context, held product.Attribution) error {
	switch a.mode.Load() {
	case 1:
		return ErrAuthorityUnavailable
	case 2:
		return product.ErrNotFound
	case 3:
		<-ctx.Done()
		return ErrAuthorityUnavailable
	default:
		return a.authority.HoldsAttribution(ctx, held)
	}
}

func TestExistingStreamBridgesOnlyBoundedAuthorityOutage(t *testing.T) {
	for _, mode := range []int32{1, 2, 3} {
		t.Run(map[int32]string{1: "unavailable", 2: "revoked", 3: "stalled"}[mode], func(t *testing.T) {
			a := &interruptedAuthority{authority: newAuthority()}
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(200)
				io.WriteString(w, "started\n")
				w.(http.Flusher).Flush()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						io.WriteString(w, "chunk\n")
						w.(http.Flusher).Flush()
					}
				}
			}))
			defer upstream.Close()
			g, err := New(a, Config{Audience: "model", Upstream: upstream.URL, MaxRequests: 2, RecheckInterval: 10 * time.Millisecond, AuthorityGrace: 300 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			server := httptest.NewServer(g)
			defer server.Close()
			req, _ := http.NewRequest("POST", server.URL, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer a")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			ended := make(chan struct{})
			go func() { io.Copy(io.Discard, response.Body); close(ended) }()
			a.mode.Store(1)
			// No fresh request inherits cached authority during an owner outage.
			probe, _ := http.NewRequest("POST", server.URL, strings.NewReader("{}"))
			probe.Header.Set("Authorization", "Bearer b")
			denied, err := http.DefaultClient.Do(probe)
			if err != nil {
				t.Fatal(err)
			}
			denied.Body.Close()
			if denied.StatusCode != 503 {
				t.Fatal(denied.StatusCode)
			}
			select {
			case <-ended:
				t.Fatal("short outage killed stream")
			case <-time.After(80 * time.Millisecond):
			}
			a.mode.Store(0)
			select {
			case <-ended:
				t.Fatal("recovery killed stream")
			case <-time.After(350 * time.Millisecond):
			}
			if requests.Load() != 1 {
				t.Fatal("stream resubmitted", requests.Load())
			}
			started := time.Now()
			a.mode.Store(mode)
			limit := 500 * time.Millisecond
			if mode == 2 {
				limit = 150 * time.Millisecond
			}
			select {
			case <-ended:
			case <-time.After(limit):
				t.Fatal("authority deadline/revocation not enforced", time.Since(started))
			}
		})
	}
}

func unixAuthorityFixture(t *testing.T, handler http.Handler) (string, *http.Server) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "authority.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return socket, server
}

func awaitPending(t *testing.T, a *RemoteAuthority, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.Pending() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pending requests", a.Pending(), "want", want)
}

func TestRemoteAuthorityRetainsSlotsThroughLostRepliesAndCleanup(t *testing.T) {
	var releaseUnavailable, dropAcquire atomic.Bool
	var acquired, closed atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c authorityCommand
		if json.NewDecoder(r.Body).Decode(&c) != nil {
			t.Error("decode")
			w.WriteHeader(400)
			return
		}
		var out any = struct{}{}
		switch strings.TrimPrefix(r.URL.Path, AuthorityPath) {
		case "registration":
			out = product.GatewayRegistration{}
		case "register":
			out = product.GatewayRegistration{Instance: c.Request.Instance, Generation: 1, Slots: 1}
		case "acquire":
			acquired.Store(c.Request.Sequence)
			if dropAcquire.Load() {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
			out = product.Attribution{Grant: product.ServiceGrant{ID: "grant", Audience: "model"}, TurnID: "turn", Request: &c.Request}
		case "release":
			if releaseUnavailable.Load() {
				w.WriteHeader(503)
				return
			}
			closed.Store(c.Request.Sequence)
		case "check":
			if closed.Load() >= c.Request.Sequence {
				w.WriteHeader(403)
				return
			}
		default:
			t.Error("unknown route", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(out)
	})
	socket, _ := unixAuthorityFixture(t, handler)
	a, err := NewRemoteAuthority(context.Background(), socket, "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first, release, err := a.AcquireService(context.Background(), "model", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.HoldsAttribution(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	releaseUnavailable.Store(true)
	release()
	release()
	_, _, err = a.AcquireService(context.Background(), "model", "secret")
	if !errors.Is(err, product.ErrCapacity) {
		t.Fatal("cleanup freed an unacknowledged slot", err)
	}
	releaseUnavailable.Store(false)
	awaitPending(t, a, 0)
	dropAcquire.Store(true)
	releaseUnavailable.Store(true)
	_, _, err = a.AcquireService(context.Background(), "model", "secret")
	if !errors.Is(err, ErrAuthorityUnavailable) {
		t.Fatal(err)
	}
	if acquired.Load() != 2 || a.Pending() != 1 {
		t.Fatal(acquired.Load(), a.Pending())
	}
	releaseUnavailable.Store(false)
	awaitPending(t, a, 0)
	if closed.Load() != 2 {
		t.Fatal("lost acquire was not settled", closed.Load())
	}
	dropAcquire.Store(false)
	third, release, err := a.AcquireService(context.Background(), "model", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if third.Request.Sequence != 3 {
		t.Fatal(third)
	}
	release()
	awaitPending(t, a, 0)
}

func TestAuthorityProtocolRejectsUndeclaredAudienceAndUnknownVersion(t *testing.T) {
	store, err := product.Open(context.Background(), filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := AuthorityHandler(store, map[string]int{"model": 1})
	socket, _ := unixAuthorityFixture(t, handler)
	_, err = NewRemoteAuthority(context.Background(), socket, "undeclared", 1)
	if !errors.Is(err, product.ErrNotFound) {
		t.Fatal(err)
	}
	a, err := NewRemoteAuthority(context.Background(), socket, "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _, err = a.AcquireService(context.Background(), "model", "not-a-credential")
	if !errors.Is(err, product.ErrNotFound) {
		t.Fatal(err)
	}
	awaitPending(t, a, 0)
	request := httptest.NewRequest("POST", "/gateway/v2/check", strings.NewReader(`{"request":{"audience":"model"}}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatal(response.Code)
	}
}
