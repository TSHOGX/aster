package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/modelconn"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/runner"
	"aster/evolution-space/internal/workflow"
)

const seal = "a-host-secret-of-more-than-thirty-two-characters"

func ptr(s string) *string { return &s }
func input(base string, key *string) modelconn.Input {
	return modelconn.Input{Name: "Team GLM", Enabled: true, Settings: modelconn.Settings{BaseURL: base, Model: "glm-5.2", MaxOutputTokens: 4000, JSONMode: true}, Policy: modelconn.Policy{CallTokens: 8000, TimeoutSeconds: 30, Concurrency: 2}, Key: key}
}

type fakeService struct {
	calls   atomic.Int32
	replies []func(w http.ResponseWriter, r *http.Request)
	gate    chan struct{}
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(f.calls.Add(1)) - 1
	// Consume the body first so the server notices a cancelled client.
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if r.Header.Get("Authorization") != "Bearer sk-test-key" {
		w.WriteHeader(401)
		return
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-r.Context().Done():
			return
		}
	}
	f.replies[min(n, len(f.replies)-1)](w, r)
}
func reply(content string, tokens int64) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model     string `json:"model"`
			MaxTokens int64  `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "req-1", "model": body.Model, "choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]int64{"prompt_tokens": tokens / 2, "completion_tokens": tokens / 2, "total_tokens": tokens}})
	}
}

var itemsSchema = json.RawMessage(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`)

func fixture(t *testing.T, service http.Handler) (*Adapter, evolution.Revision, func(string, string) (runner.ModelCallKey, runner.ModelSubmission)) {
	t.Helper()
	ctx := context.Background()
	server := httptest.NewServer(service)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	store, err := modelconn.Open(ctx, filepath.Join(dir, "modelconn.db"), seal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	projectionStore, err := projections.Open(ctx, filepath.Join(dir, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projectionStore.Close() })
	c, err := store.Save(ctx, "admin", "team-glm", input(server.URL+"/v1", ptr("sk-test-key")))
	if err != nil {
		t.Fatal(err)
	}
	digest := evolution.Digest(itemsSchema)
	adapter := NewAdapter(store, projectionStore, func(d string) (json.RawMessage, bool) { return itemsSchema, d == digest })
	newCall := func(run, call string) (runner.ModelCallKey, runner.ModelSubmission) {
		key := runner.ModelCallKey{RunID: run, CallID: call, IdempotencyKey: evolution.Digest([]byte(run + call)), RequestDigest: evolution.Digest([]byte("request" + call)), Model: c.Current}
		node := evolution.Node{ID: "extract", Kind: "agent", OutputPorts: map[string]string{"state": digest}, ModelRef: &c.Current}
		return key, runner.ModelSubmission{RunID: run, CallID: call, IdempotencyKey: key.IdempotencyKey, RequestDigest: key.RequestDigest, Model: c.Current, Call: workflow.Call{ID: call, Node: node, Prompt: "Extract items"}, Ceiling: projections.RunUsage{Tokens: 8000}}
	}
	return adapter, c.Current, newCall
}
func decode(t *testing.T, raw []byte) runner.ModelObservation {
	t.Helper()
	var value runner.ModelObservation
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAdapterCompletesValidatesRepairsAndNeverResubmits(t *testing.T) {
	ctx := context.Background()
	service := &fakeService{replies: []func(http.ResponseWriter, *http.Request){reply("```json\n{\"state\":{\"items\":[1]}}\n```", 100), reply(`{"state":{"items":["a"]}}`, 60)}}
	adapter, ref, newCall := fixture(t, service)
	model, ok := adapter.Model(ctx, ref)
	if !ok || model.Disabled || model.Ceiling.Tokens != 8000 || len(adapter.Choices(ctx)) != 1 {
		t.Fatal("model source", model, ok)
	}
	key, submission := newCall("run-1", "call-1")
	if _, err := adapter.Lookup(ctx, key); !errors.Is(err, projections.ErrProviderCallNotFound) {
		t.Fatal("absent call", err)
	}
	value := decode(t, must(adapter.Submit(ctx, key, submission)))
	if value.State != "completed" || string(value.Outputs["state"]) != `{"items":["a"]}` || value.Usage.Tokens != 160 || value.ProviderRequestID != "req-1" || service.calls.Load() != 2 {
		t.Fatal("repaired completion", value, service.calls.Load())
	}
	again := decode(t, must(adapter.Submit(ctx, key, submission)))
	looked := decode(t, must(adapter.Lookup(ctx, key)))
	if again.Usage != value.Usage || looked.State != "completed" || service.calls.Load() != 2 {
		t.Fatal("replay must not resubmit", again, looked)
	}
	other := key
	other.RequestDigest = evolution.Digest([]byte("different"))
	if _, err := adapter.Lookup(ctx, other); !errors.Is(err, runner.ErrModelDenied) {
		t.Fatal("different request matched retained call", err)
	}
}

func TestAdapterFailureInterruptionCancellationAndDisabledConnections(t *testing.T) {
	ctx := context.Background()
	service := &fakeService{replies: []func(http.ResponseWriter, *http.Request){func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }}}
	adapter, ref, newCall := fixture(t, service)
	key, submission := newCall("run-1", "unavailable")
	value := decode(t, must(adapter.Submit(ctx, key, submission)))
	if value.State != "failed" || !value.Retryable || value.Usage.Tokens != 0 || !strings.Contains(value.Error, "503") {
		t.Fatal("service error", value)
	}
	// A call left running by a previous process is settled conservatively.
	key, submission = newCall("run-1", "interrupted")
	if err := adapter.Store.InsertRunning(ctx, storeKey(key), submission.Ceiling.Tokens, submission.Ceiling.CostMicros); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart: the reopened store has a new process identity.
	adapter.Store = reopen(t, adapter.Store)
	value = decode(t, must(adapter.Lookup(ctx, key)))
	if value.State != "failed" || !value.Retryable || value.Usage.Tokens != 8000 || !strings.Contains(value.Error, "interrupted") {
		t.Fatal("interrupted call", value)
	}
	key, _ = newCall("run-1", "never-sent")
	value = decode(t, must(adapter.Cancel(ctx, key)))
	if value.State != "cancelled" || value.Usage.Tokens != 0 {
		t.Fatal("cancel before dispatch", value)
	}
	// Cancelling an in-flight call stops it and counts the reserved ceiling.
	service.gate = make(chan struct{})
	key, submission = newCall("run-1", "in-flight")
	result := make(chan []byte, 1)
	go func() { raw, _ := adapter.Submit(ctx, key, submission); result <- raw }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if row, err := adapter.Store.Call(ctx, storeKey(key)); err == nil && row.State == "running" {
			break
		} else if err != nil && !errors.Is(err, modelconn.ErrCallNotFound) || time.Now().After(deadline) {
			t.Fatal("call never started", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	value = decode(t, must(adapter.Cancel(ctx, key)))
	if value.State != "cancelled" || value.Usage.Tokens != 8000 {
		t.Fatal("cancel in flight", value)
	}
	if submitted := decode(t, <-result); submitted.State != "cancelled" {
		t.Fatal("submit result after cancel", submitted)
	}
	service.gate = nil
	disabled := input("http://127.0.0.1:1/v1", nil)
	disabled.ExpectedRevision, disabled.Enabled = 1, false
	c, _ := adapter.Store.Get(ctx, ref.ID)
	disabled.Settings = c.Settings
	disabled.Key = nil
	if _, err := adapter.Store.Save(ctx, "admin", ref.ID, disabled); err != nil {
		t.Fatal(err)
	}
	if model, ok := adapter.Model(ctx, ref); !ok || !model.Disabled || len(adapter.Choices(ctx)) != 0 {
		t.Fatal("disabled connection still offered", model)
	}
	key, submission = newCall("run-2", "disabled")
	value = decode(t, must(adapter.Submit(ctx, key, submission)))
	if value.State != "failed" || value.Retryable || value.Usage.Tokens != 0 {
		t.Fatal("disabled connection dispatched", value)
	}
}

func must(raw []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return raw
}

func reopen(t *testing.T, store *modelconn.Store) *modelconn.Store {
	t.Helper()
	path := store.Path()
	store.Close()
	next, err := modelconn.Open(context.Background(), path, seal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	return next
}
