package khora

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

type engineFixture struct {
	mu                         sync.Mutex
	storeID, version           string
	manual                     bool
	state, placement, revision string
	epoch                      uint64
	createMissing, failStop    bool
	calls                      int
	keys                       []string
	requests                   []map[string]any
}

func fixture(t *testing.T) (*Backend, *engineFixture) {
	t.Helper()
	engine := &engineFixture{storeID: "store", version: EngineRevision, manual: true, state: "running", placement: "active", revision: strings.Repeat("a", 64), epoch: 1}
	server := httptest.NewServer(http.HandlerFunc(engine.serve))
	t.Cleanup(server.Close)
	backend, err := New(context.Background(), Config{Endpoint: server.URL, StoreID: "store"})
	if err != nil {
		t.Fatal(err)
	}
	return backend, engine
}
func (e *engineFixture) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	switch {
	case r.URL.Path == "/v1/info":
		write(map[string]any{"store_id": e.storeID, "version": e.version, "manual_cold_recovery": e.manual})
	case r.URL.Path == "/v1/sandboxes" && r.Method == http.MethodPost:
		e.calls++
		e.keys = append(e.keys, r.Header.Get("Idempotency-Key"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		e.requests = append(e.requests, body)

		w.WriteHeader(http.StatusCreated)
		write(map[string]string{"sandbox_id": "sandbox"})
	case r.URL.Path == "/v1/sandboxes/sandbox" && r.Method == http.MethodGet:
		if e.createMissing {
			http.Error(w, "not found", 404)
			return
		}
		write(map[string]any{"sandbox_id": "sandbox", "state": e.state, "placement_epoch": e.epoch, "placement_state": e.placement, "mutation_revision": e.revision})
	case r.URL.Path == "/v1/sandboxes/sandbox/pause":
		e.calls++
		if r.Header.Get("If-Match") != "\""+e.revision+"\"" {
			http.Error(w, "stale", 412)
			return
		}
		e.state = "paused"
		e.placement = "lost"
		if e.failStop {
			e.placement = "stopping"
		}
		e.revision = strings.Repeat("b", 64)
		write(map[string]string{"sandbox_id": "sandbox", "state": e.state})
	case r.URL.Path == "/v1/sandboxes/sandbox/resume":
		e.calls++
		if r.Header.Get("If-Match") != "\""+e.revision+"\"" {
			http.Error(w, "stale", 412)
			return
		}
		e.state = "running"
		e.placement = "active"
		e.epoch++
		e.revision = strings.Repeat("c", 64)
		write(map[string]string{"sandbox_id": "sandbox", "state": e.state})
	default:
		http.Error(w, "not found", 404)
	}
}
func createRequest() sandbox.CreateRequest {
	return sandbox.CreateRequest{IdempotencyKey: "stable-key", TemplateID: "runtime-template", CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 1 << 30, Mounts: []sandbox.Mount{{VolumeID: "workspace", Destination: "/workspace", Access: "exclusive"}}}
}

func TestCreateInspectsBindingAndPreservesDurableKeyAfterLostResponse(t *testing.T) {
	backend, engine := fixture(t)
	backend.httpClient.Transport = &lostCreateResponse{base: http.DefaultTransport}
	if _, err := backend.Create(context.Background(), createRequest()); !sandbox.Is(err, sandbox.Unavailable) {
		t.Fatalf("lost response: %v", err)
	}
	observed, err := backend.Create(context.Background(), createRequest())
	if err != nil {
		t.Fatal(err)
	}
	if observed.StoreID != "store" || observed.SandboxID != "sandbox" || observed.Epoch != 1 || observed.CPU != nil || observed.Memory != nil {
		t.Fatalf("fabricated binding or telemetry: %+v", observed)
	}
	if len(engine.keys) != 2 || engine.keys[0] != engine.keys[1] || engine.keys[0] != "stable-key" {
		t.Fatalf("retry changed key: %v", engine.keys)
	}
	for _, request := range engine.requests {
		if request["idle_timeout"] != "0s" || request["retention"] != "0s" {
			t.Fatalf("engine timers can destroy retained history: %+v", request)
		}
	}
}

func TestCreateResponseAloneDoesNotEstablishEnvironment(t *testing.T) {
	backend, engine := fixture(t)
	engine.createMissing = true
	if _, err := backend.Create(context.Background(), createRequest()); !sandbox.Is(err, sandbox.NotFound) {
		t.Fatalf("bound stale create response: %v", err)
	}
}

func TestBackendRebuildRefusedBeforeMutation(t *testing.T) {
	backend, engine := fixture(t)
	reference := sandbox.Reference{StoreID: "store", SandboxID: "sandbox", MutationRevision: engine.revision, Epoch: 1}
	engine.storeID = "rebuilt"
	if _, err := backend.Pause(context.Background(), reference); !sandbox.Is(err, sandbox.IdentityChanged) {
		t.Fatalf("changed identity became absence/success: %v", err)
	}
	if engine.calls != 0 {
		t.Fatal("mutation reached rebuilt engine")
	}
}

func TestPauseRequiresWriterTerminationAndResumeAdvancesEpoch(t *testing.T) {
	backend, engine := fixture(t)
	reference := sandbox.Reference{StoreID: "store", SandboxID: "sandbox", MutationRevision: engine.revision, Epoch: 1}
	paused, err := backend.Pause(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Pause(context.Background(), reference); !sandbox.Is(err, sandbox.Precondition) {
		t.Fatalf("old pause revision silently refreshed: %v", err)
	}
	resumed, err := backend.Resume(context.Background(), paused.Reference)
	if err != nil || resumed.Epoch <= reference.Epoch {
		t.Fatalf("resume binding: %+v %v", resumed, err)
	}
	engine.failStop = true
	if _, err := backend.Pause(context.Background(), resumed.Reference); !sandbox.Is(err, sandbox.Uncertain) {
		t.Fatalf("paused status treated as writer absence: %v", err)
	}
}

func TestEngineCompatibilityAndRemoteTransportFailClosed(t *testing.T) {
	backend, engine := fixture(t)
	engine.manual = false
	if _, err := backend.Identity(context.Background()); !sandbox.Is(err, sandbox.Incompatible) {
		t.Fatalf("autonomous replacement accepted: %v", err)
	}
	if _, err := New(context.Background(), Config{Endpoint: "http://192.0.2.1:8080"}); !sandbox.Is(err, sandbox.Invalid) {
		t.Fatalf("remote unauthenticated HTTP accepted: %v", err)
	}
	if _, err := New(context.Background(), Config{Endpoint: "http://127.0.0.1:8080", Version: "old"}); !sandbox.Is(err, sandbox.Incompatible) {
		t.Fatalf("old build accepted: %v", err)
	}
}

func TestEngineErrorClassesRemainDistinct(t *testing.T) {
	for code, class := range map[int]sandbox.ErrorClass{400: sandbox.Invalid, 404: sandbox.NotFound, 409: sandbox.Conflict, 412: sandbox.Precondition, 507: sandbox.Capacity, 503: sandbox.Unavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/info" {
					json.NewEncoder(w).Encode(map[string]any{"store_id": "store", "version": EngineRevision, "manual_cold_recovery": true})
					return
				}
				http.Error(w, "synthetic engine refusal", code)
			}))
			defer server.Close()
			backend, err := New(context.Background(), Config{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := backend.Create(context.Background(), createRequest()); !sandbox.Is(err, class) {
				t.Fatalf("status %d mapped to %v", code, err)
			}
		})
	}
}

type lostCreateResponse struct {
	base http.RoundTripper
	lost bool
}

func (t *lostCreateResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil && !t.lost && request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes" {
		t.lost = true
		response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return response, err
}
