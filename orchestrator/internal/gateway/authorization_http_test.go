package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

type authorizerFixture struct {
	requests chan egress.AuthorizationRequest
}

func (a *authorizerFixture) AuthorizeEgressRequest(_ context.Context, p egress.AuthorizationRequest) (egress.Authorization, error) {
	a.requests <- p
	return egress.Authorization{RequestContext: egress.RequestContext{RequestID: p.RequestID}, ExpiresAt: p.Now.Add(time.Minute)}, nil
}
func (a *authorizerFixture) FinishEgressRequest(context.Context, egress.RequestFinish) error {
	return nil
}

func TestAuthorizationControlUsesUnixPeerIdentityAndHostClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorize.sock")
	listener, err := ListenAuthorization(path, os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	fixture := &authorizerFixture{requests: make(chan egress.AuthorizationRequest, 1)}
	server := &http.Server{Handler: AuthorizationHandler(fixture, uint32(os.Getuid())), ConnContext: PeerContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { server.Close(); <-done }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	body, _ := json.Marshal(egress.AuthorizationRequest{RequestID: "request", Now: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)})
	response, err := client.Post("http://unix/v1/authorize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("trusted Unix gateway rejected: %d", response.StatusCode)
	}
	request := <-fixture.requests
	if time.Since(request.Now) > time.Second {
		t.Fatal("gateway chose the authorization clock")
	}
	if other, err := ListenAuthorization(path, os.Getgid()); err == nil {
		other.Close()
		t.Fatal("existing authorization socket replaced")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0660 {
		t.Fatalf("socket access mode: %v %v", info, err)
	}
}
func TestAuthorizationHeadersCannotForgeKernelPeer(t *testing.T) {
	fixture := &authorizerFixture{requests: make(chan egress.AuthorizationRequest, 1)}
	request := httptest.NewRequest(http.MethodPost, "/v1/authorize", bytes.NewBufferString(`{"RequestID":"forged"}`))
	request.Header.Set("X-Gateway-UID", "1234")
	request.Header.Set("X-Original-Destination", "10.80.0.1:443")
	response := httptest.NewRecorder()
	AuthorizationHandler(fixture, 1234).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forged peer accepted: %d", response.Code)
	}
	select {
	case <-fixture.requests:
		t.Fatal("untrusted request reached authorizer")
	default:
	}
}
