package khora

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestExpiredEventCursorReadsTypedWindowFromFreshRequest(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/info" {
			json.NewEncoder(w).Encode(map[string]any{"store_id": "engine", "version": EngineRevision, "manual_cold_recovery": true})
			return
		}
		cursors = append(cursors, r.URL.Query().Get("after"))
		if r.URL.Query().Get("after") == "2" {
			w.WriteHeader(410)
			w.Write([]byte(`{"earliest":5,"latest":8,"events":[]}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"earliest": 5, "latest": 9, "events": []map[string]any{{"seq": 5, "id": "historical", "sandbox_id": "sandbox", "kind": "resumed", "state": "running", "placement_epoch": 2}}})
	}))
	defer server.Close()
	backend, err := New(context.Background(), Config{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	page, err := backend.Events(context.Background(), 2, 128)
	var expired *sandbox.EventWindowExpired
	if !errors.As(err, &expired) || page.Earliest != 5 || page.Latest != 9 || expired.Page.Latest != 9 {
		t.Fatalf("lost typed event bounds: %+v %v", page, err)
	}
	if !reflect.DeepEqual(cursors, []string{"2", ""}) {
		t.Fatalf("SDK error body used instead of typed window: %v", cursors)
	}
}
