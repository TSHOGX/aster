package product_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"harness-platform/orchestrator/internal/servicegateway"
)

func TestPlatformRealGatewayOwnerReceiptSurvivesLostReply(t *testing.T) {
	store, scope, token := product.PlatformFixtureForTest(t)
	var drop atomic.Bool
	mux := http.NewServeMux()
	mux.Handle(servicegateway.AuthorityPath, servicegateway.AuthorityHandler(store, map[string]int{"model": 2}))
	owner := servicegateway.PlatformHandler(store, map[string]bool{"model": true})
	mux.Handle(servicegateway.PlatformOwnerPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop.Swap(false) {
			recorded := httptest.NewRecorder()
			owner.ServeHTTP(recorded, r)
			if recorded.Code != 200 {
				t.Error("expected committed operation", recorded.Code, recorded.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		owner.ServeHTTP(w, r)
	}))
	socket := filepath.Join(t.TempDir(), "owner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	authority, err := servicegateway.NewRemoteAuthority(context.Background(), socket, "model", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls.Add(1); w.WriteHeader(200) }))
	defer upstream.Close()
	gateway, err := servicegateway.New(authority, servicegateway.Config{Audience: "model", Upstream: upstream.URL, APIKey: "provider-secret", MaxRequests: 2, Platform: authority})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	guest := httptest.NewServer(gateway)
	defer guest.Close()
	call := func(path string, input any) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(input)
		req, _ := http.NewRequest("POST", guest.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Project-Id", "foreign")
		response, e := guest.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte("provider-secret")) {
			t.Fatal("credential leaked")
		}
		return response.StatusCode, raw
	}
	status, raw := call(servicegateway.PlatformPath, product.PlatformScheduleRequest{Action: "context"})
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var contextResult product.PlatformScheduleResult
	if err = json.Unmarshal(raw, &contextResult); err != nil {
		t.Fatal(err)
	}
	title, prompt, kind, zone, at := "Later", "Read the result", "at", "UTC", time.Now().Add(time.Hour).Format(time.RFC3339)
	input := product.PlatformScheduleRequest{Action: "execute", Operation: "create", ID: "transport-task", RequestID: "native-invocation", ExpectedRunID: contextResult.Context.RunID, ToolCallID: "provider-call", Patch: product.SchedulePatch{Title: &title, Prompt: &prompt, TriggerKind: &kind, Timezone: &zone, RunAt: &at}}
	drop.Store(true)
	status, _ = call(servicegateway.PlatformPath, input)
	if status != 503 {
		t.Fatal("lost reply should be uncertain", status)
	}
	status, raw = call(servicegateway.PlatformPath, input)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var receipt product.PlatformScheduleResult
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	task, err := store.Schedule(context.Background(), scope, input.ID)
	if err != nil || task.ID != receipt.Receipt.Schedule.ID || task.OriginRunID != input.ExpectedRunID {
		t.Fatal(task, err)
	}
	status, replay := call(servicegateway.PlatformPath, input)
	if status != 200 || string(replay) != string(raw) {
		t.Fatal("receipt changed", status, string(replay))
	}
	changed := input
	changed.Patch.Title = &prompt
	status, _ = call(servicegateway.PlatformPath, changed)
	if status != 409 {
		t.Fatal("changed payload accepted", status)
	}
	for _, path := range []string{"/platform", "/platform/", "/platform/v2/schedules", "/platform/v1/schedules/", "/platform/v1/unknown"} {
		status, _ = call(path, map[string]string{"secret": "private prompt"})
		if status == 200 {
			t.Fatal(path)
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("platform request reached provider")
	}
	status, _ = call("/v1/messages", map[string]string{"model": "normal"})
	if status != 200 || upstreamCalls.Load() != 1 {
		t.Fatal("model forwarding changed", status)
	}
	// Strict owner decoding rejects guest authority fields and excessive bodies.
	out := httptest.NewRecorder()
	owner.ServeHTTP(out, httptest.NewRequest("POST", servicegateway.PlatformOwnerPath, strings.NewReader(`{"request":{},"input":{"action":"context","project_id":"foreign"}}`)))
	if out.Code != 400 {
		t.Fatal(out.Code)
	}
}
