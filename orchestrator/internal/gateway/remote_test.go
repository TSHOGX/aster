package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

func serveControl(t *testing.T, path string, handler http.Handler) func() {
	t.Helper()
	listener, err := ListenControl(path, os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ConnContext: PeerContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var once sync.Once
	stop := func() { once.Do(func() { _ = server.Close(); <-done }) }
	t.Cleanup(stop)
	return stop
}

func controlFixture(t *testing.T, timeout time.Duration) (*Flows, *Remote, func()) {
	return namedControlFixture(t, "model", timeout)
}
func namedControlFixture(t *testing.T, id string, timeout time.Duration) (*Flows, *Remote, func()) {
	t.Helper()
	flows := NewFlows()
	handler, err := ControlHandler(flows, ControlOptions{ID: id, ApplicationUID: uint32(os.Getuid()), MaxOperationTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control.sock")
	stop := serveControl(t, path, handler)
	remote, err := NewRemote(context.Background(), RemoteConfig{ID: id, Socket: path, PeerUID: uint32(os.Getuid()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remote.Close)
	return flows, remote, stop
}

func beginRemoteFlow(t *testing.T, flows *Flows, binding, grant, request string) (context.Context, *Flow) {
	t.Helper()
	ctx, flow, err := flows.Begin(context.Background(), egress.Authorization{RequestContext: egress.RequestContext{BindingID: binding, BindingRevision: 1, GrantID: grant, RequestID: request}, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Finish)
	return ctx, flow
}

func TestRemoteRevocationWaitsForActualUpstreamFinishAndRetriesTimeout(t *testing.T) {
	flows, remote, _ := controlFixture(t, 100*time.Millisecond)
	ctx := context.Background()
	if err := remote.Activate(ctx, "route", 1); err != nil {
		t.Fatal(err)
	}
	upstream, flow := beginRemoteFlow(t, flows, "route", "grant", "request")
	result := make(chan error, 1)
	go func() { result <- remote.RevokeGrant(ctx, "grant") }()
	select {
	case <-upstream.Done():
	case <-time.After(time.Second):
		t.Fatal("remote revocation did not cancel upstream")
	}
	if err := <-result; err == nil {
		t.Fatal("cancellation alone acknowledged teardown")
	}
	if _, _, err := flows.Begin(ctx, egress.Authorization{RequestContext: egress.RequestContext{BindingID: "route", BindingRevision: 1, GrantID: "grant", RequestID: "new"}, ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("unsettled revocation admitted new flow: %v", err)
	}
	flow.Finish()
	for range 2 {
		if err := remote.RevokeGrant(ctx, "grant"); err != nil {
			t.Fatalf("completed drain retry: %v", err)
		}
	}
	_, later := beginRemoteFlow(t, flows, "route", "later-grant", "later-request")
	later.Finish()
	if err := remote.Revoke(ctx, "route"); err != nil {
		t.Fatal(err)
	}
	if err := remote.Activate(ctx, "route", 1); err == nil {
		t.Fatal("remote activation revived revoked binding")
	}
}

func TestRemoteControlRejectsWrongKernelPeerAndIdentity(t *testing.T) {
	_, remote, _ := controlFixture(t, time.Second)
	for name, change := range map[string]func(*RemoteConfig){
		"uid": func(p *RemoteConfig) { p.PeerUID++ },
		"id":  func(p *RemoteConfig) { p.ID = "another" },
	} {
		t.Run(name, func(t *testing.T) {
			p := remote.config
			change(&p)
			if other, err := NewRemote(context.Background(), p); err == nil {
				other.Close()
				t.Fatal("untrusted control accepted")
			}
		})
	}
	handler, err := ControlHandler(NewFlows(), ControlOptions{ID: "forgery", ApplicationUID: 1234, MaxOperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	request.Header.Set("X-Application-UID", "1234")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("header impersonated kernel peer")
	}
}

func TestRemoteGatewayRestartCannotAcknowledgeOldInstanceWork(t *testing.T) {
	_, remote, stop := controlFixture(t, time.Second)
	original := remote.Info()
	stop()
	newFlows := NewFlows()
	handler, err := ControlHandler(newFlows, ControlOptions{ID: "model", ApplicationUID: uint32(os.Getuid()), MaxOperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	serveControl(t, remote.config.Socket, handler)
	remote.transport.CloseIdleConnections() // Force a new Unix connection; keep the original instance pin.
	if err := remote.Revoke(context.Background(), "old-route"); !errors.Is(err, ErrGatewayRestarted) {
		t.Fatalf("new process acknowledged old upstream termination: %v", err)
	}
	if err := newFlows.Activate("old-route", 1); err != nil {
		t.Fatal("stale instance mutation reached replacement registry")
	}
	replacement, err := NewRemote(context.Background(), remote.config)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if replacement.Info().Instance == original.Instance {
		t.Fatal("gateway restart reused old process identity")
	}
}

func TestControlRequestsAreStrictAndBounded(t *testing.T) {
	flows, remote, _ := controlFixture(t, time.Second)
	for _, body := range []string{
		`{"binding_id":"route","unexpected":true}`,
		`{"binding_id":"route"} {}`,
		`{"binding_id":"route","grant_id":"unrelated"}`,
		`{"binding_id":"route","revision":1}`,
		`{"binding_id":"` + strings.Repeat("a", 8192) + `"}`,
	} {
		request, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/bindings/revoke", strings.NewReader(body))
		request.Header.Set(gatewayInstanceHeader, remote.Info().Instance)
		response, err := remote.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid body accepted: status %d", response.StatusCode)
		}
	}
	if err := flows.Activate("route", 1); err != nil {
		t.Fatal("rejected request partially revoked a route")
	}
}

func TestRemoteControlDoesNotFollowRedirectOrAcceptUnexpectedSuccess(t *testing.T) {
	_, remote, stop := controlFixture(t, time.Second)
	stop()
	var requests atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusTemporaryRedirect)
	serveControl(t, remote.config.Socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set(gatewayInstanceHeader, remote.Info().Instance)
		w.Header().Set("Location", "http://127.0.0.1:1/forbidden")
		w.WriteHeader(int(status.Load()))
	}))
	remote.transport.CloseIdleConnections()
	if err := remote.Revoke(context.Background(), "route"); err == nil || requests.Load() != 1 {
		t.Fatalf("redirect followed or considered teardown: %d %v", requests.Load(), err)
	}
	status.Store(http.StatusOK)
	if err := remote.Revoke(context.Background(), "route"); err == nil {
		t.Fatal("non-contract success counted as drain")
	}
}

func TestRemoteControlsOnlyDeclaredNextHop(t *testing.T) {
	firstFlows, first, _ := namedControlFixture(t, "first", 100*time.Millisecond)
	secondFlows, second, _ := namedControlFixture(t, "second", time.Second)
	ctx := context.Background()
	directory := mapDirectory{"route-first": "first", "route-second": "second"}
	remotes, err := NewRemotes(ctx, []RemoteConfig{first.config, second.config}, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer remotes.Close()
	if err := remotes.Activate(ctx, "route-first", 1); err != nil {
		t.Fatal(err)
	}
	if err := remotes.Activate(ctx, "route-second", 1); err != nil {
		t.Fatal(err)
	}
	_, blocked := beginRemoteFlow(t, firstFlows, "route-first", "grant-first", "blocked")
	secondContext, finished := beginRemoteFlow(t, secondFlows, "route-second", "grant-second", "finished")
	defer finished.Finish()
	if err := remotes.Revoke(ctx, "route-first"); err == nil {
		t.Fatal("unresolved flow counted as drained")
	}
	if secondContext.Err() != nil {
		t.Fatal("unrelated next hop was revoked")
	}
	blocked.Finish()
	if err := remotes.Revoke(ctx, "route-first"); err != nil {
		t.Fatal(err)
	}
	if err := remotes.Revoke(ctx, "unknown"); err == nil {
		t.Fatal("unknown binding drained without authority")
	}
}

func TestRemoteRetrySettlesLostSuccessfulDrainReply(t *testing.T) {
	flows := NewFlows()
	handler, err := ControlHandler(flows, ControlOptions{ID: "data", ApplicationUID: uint32(os.Getuid()), MaxOperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var lose atomic.Bool
	lose.Store(true)
	path := filepath.Join(t.TempDir(), "data.sock")
	serveControl(t, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/bindings/revoke" && lose.CompareAndSwap(true, false) {
			committed := httptest.NewRecorder()
			handler.ServeHTTP(committed, r)
			if committed.Code == http.StatusNoContent {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	remote, err := NewRemote(context.Background(), RemoteConfig{ID: "data", Socket: path, PeerUID: uint32(os.Getuid()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if err := remote.Activate(context.Background(), "route", 1); err != nil {
		t.Fatal(err)
	}
	if err := remote.Revoke(context.Background(), "route"); err == nil {
		t.Fatal("lost drain reply reported success")
	}
	if err := flows.Activate("route", 1); !errors.Is(err, ErrRevoked) {
		t.Fatal("lost reply rolled back committed revocation")
	}
	if err := remote.Revoke(context.Background(), "route"); err != nil {
		t.Fatalf("idempotent retry did not settle lost reply: %v", err)
	}
}

func TestRemoteDrainClosesRegistryAndClientCloseOnlyDetaches(t *testing.T) {
	flows, remote, _ := controlFixture(t, time.Second)
	if err := remote.Activate(context.Background(), "route", 1); err != nil {
		t.Fatal(err)
	}
	upstream, flow := beginRemoteFlow(t, flows, "route", "grant", "request")
	remote.transport.CloseIdleConnections()
	select {
	case <-upstream.Done():
		t.Fatal("client detachment canceled upstream work")
	default:
	}
	go func() { <-upstream.Done(); flow.Finish() }()
	if err := remote.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := flows.Activate("another-route", 1); !errors.Is(err, ErrRevoked) {
		t.Fatal("drained gateway admitted new bindings")
	}
}

func TestRemoteSetRequiresExplicitUniqueGatewayConfiguration(t *testing.T) {
	_, remote, _ := controlFixture(t, time.Second)
	ctx := context.Background()
	for _, configs := range [][]RemoteConfig{{remote.config, remote.config}, {{ID: "bad", Socket: "relative", Timeout: time.Second}}} {
		if controls, err := NewRemotes(ctx, configs, mapDirectory{"unissued-grant": "model"}); err == nil {
			controls.Close()
			t.Fatal("invalid remote set accepted")
		}
	}
	controls, err := NewRemotes(ctx, []RemoteConfig{remote.config}, mapDirectory{"unissued-grant": "model"})
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err := controls.RevokeGrant(ctx, "unissued-grant"); err != nil {
		t.Fatal(err)
	}
}

type mapDirectory map[string]string

func (d mapDirectory) BindingNextHop(_ context.Context, id string) (string, error) {
	if hop := d[id]; hop != "" {
		return hop, nil
	}
	return "", egress.ErrDenied
}
func (d mapDirectory) GrantNextHop(ctx context.Context, id string) (string, error) {
	return d.BindingNextHop(ctx, id)
}
func (d mapDirectory) ActiveGatewayBindings(context.Context, string, string, int) ([]egress.BindingAuthority, error) {
	return nil, nil
}

func TestUnavailableOptionalGatewayDoesNotBlockOtherDestinations(t *testing.T) {
	_, remote, _ := controlFixture(t, time.Second)
	broken := RemoteConfig{ID: "offline", Socket: filepath.Join(t.TempDir(), "offline.sock"), PeerUID: uint32(os.Getuid()), Timeout: time.Second}
	controls, err := NewRemotes(context.Background(), []RemoteConfig{remote.config, broken}, mapDirectory{"healthy": "model", "unavailable": "offline"})
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err := controls.Activate(context.Background(), "healthy", 1); err != nil {
		t.Fatal(err)
	}
	if err := controls.Activate(context.Background(), "unavailable", 1); err == nil {
		t.Fatal("offline destination became ready")
	}
	if err := controls.Revoke(context.Background(), "healthy"); err != nil {
		t.Fatal(err)
	}
}

func (d mapDirectory) PendingGatewayGrantDrains(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (d mapDirectory) ConfirmGatewayGrantDrained(context.Context, string) error   { return nil }
func (d mapDirectory) ConfirmGatewayBindingDrained(context.Context, string) error { return nil }
