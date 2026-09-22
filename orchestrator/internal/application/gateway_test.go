package application

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
)

func TestGatewayStopRequiresCurrentQuiescenceAndLocalSettlement(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := product.Open(ctx, filepath.Join(directory, "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := &Application{Store: store}
	socket := filepath.Join(directory, "owner.sock")
	listener, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: owner.controlHandler()}
	go server.Serve(listener)
	defer server.Close()
	gateway := &GatewayApplication{config: Config{ControlSocket: socket}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stop: make(chan struct{}), control: &http.Server{}}
	call := func(body string) int {
		req := httptest.NewRequest("POST", "/stop", strings.NewReader(body))
		out := httptest.NewRecorder()
		gateway.controlHandler().ServeHTTP(out, req)
		return out.Code
	}
	if got := call(`{"owner":"release","generation":1}`); got != 409 {
		t.Fatal(got)
	}
	state, err := store.BeginReleaseDrain(ctx, "release", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if state.Generation != 1 {
		t.Fatal(state)
	}
	gateway.active = 1
	if got := call(`{"owner":"release","generation":1}`); got != 409 {
		t.Fatal(got)
	}
	if gateway.stopping {
		t.Fatal("busy gateway stopped")
	}
	gateway.active = 0
	if got := call(`{"owner":"stale","generation":1}`); got != 409 {
		t.Fatal(got)
	}
	if got := call(`{"owner":"release","generation":1}`); got != 200 {
		t.Fatal(got)
	}
	select {
	case <-gateway.stop:
	case <-time.After(time.Second):
		t.Fatal("gateway did not finish response before retirement")
	}
	called := false
	tracked := gateway.track(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	response := httptest.NewRecorder()
	tracked.ServeHTTP(response, httptest.NewRequest("POST", "/", nil))
	if called || response.Code != 503 {
		t.Fatal("request crossed stopped gateway", response.Code)
	}
}
