package khora

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTemplateRegistrationReadChecksIdentityAndAvailability(t *testing.T) {
	store := "store"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("template inspection mutated engine", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/info":
			fmt.Fprintf(w, `{"store_id":%q,"version":%q,"manual_cold_recovery":true}`, store, EngineRevision)
		case "/v1/templates/present":
			fmt.Fprint(w, `{"template_id":"present"}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"missing"}`)
		}
	}))
	defer server.Close()
	backend, err := New(context.Background(), Config{Endpoint: server.URL, StoreID: "store"})
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.RequireTemplate(context.Background(), "present"); err != nil {
		t.Fatal(err)
	}
	if err = backend.RequireTemplate(context.Background(), "missing"); !sandbox.Is(err, sandbox.NotFound) {
		t.Fatal(err)
	}
	if err = backend.RequireTemplate(context.Background(), "../present"); !sandbox.Is(err, sandbox.Invalid) {
		t.Fatal(err)
	}
	store = "replacement"
	if err = backend.RequireTemplate(context.Background(), "present"); !sandbox.Is(err, sandbox.IdentityChanged) {
		t.Fatal(err)
	}
}
