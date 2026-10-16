package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/workflow"
)

func TestNativeHTTPModelRecoversLostReplyAndRetainsActualEvidence(t *testing.T) {
	store, _, run, executor, _ := nativeFixture(t)
	ctx := context.Background()
	var modelRef evolution.Revision
	for ref := range executor.Registry.Models {
		modelRef = ref
	}
	secret := strings.Repeat("s", 32)
	var mu sync.Mutex
	var retained *ModelObservation
	submits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "space" || password != secret || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("X-Model-Digest") != modelRef.Digest {
			t.Error("model transport authority mismatch")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "PUT" {
			submission, err := evolution.Decode[ModelSubmission](r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if submission.Call.Prompt != "Read corpus" || submission.Model != modelRef || submission.Ceiling.Tokens != 10 {
				t.Error("lost actual prompt/model/ceiling")
			}
			submits++
			value := ModelObservation{RunID: submission.RunID, CallID: submission.CallID, IdempotencyKey: submission.IdempotencyKey, RequestDigest: submission.RequestDigest, Model: modelRef, ProviderRequestID: "provider-request", State: "completed", Usage: projections.RunUsage{Tokens: 4, CostMicros: 7}, Outputs: workflow.Values{"value": json.RawMessage(`"model output"`)}, ResponseModel: "model-build-2026", ToolTraceState: "complete", Tools: []ModelToolObservation{}}
			retained = &value
			w.WriteHeader(503)
			return
		}
		if retained == nil {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"model_call_not_found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(retained)
	}))
	defer server.Close()
	provider, err := NewHTTPModel(store, ModelConnection{Endpoint: server.URL, ClientID: "space", Secret: secret, Model: modelRef})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	executor.Registry.Models[modelRef] = Model{Provider: provider, Ceiling: projections.RunUsage{Tokens: 10, CostMicros: 20}}
	worker := Worker{Store: store, ID: "worker", Authority: executor.Authority, Executor: executor}
	if err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := store.Run(ctx, run.GroupID, run.ID)
	if err != nil || current.State != "completed" || submits != 1 {
		t.Fatal(current, submits, err)
	}
	budget, err := store.RunBudget(ctx, run.GroupID, run.ID)
	if err != nil || budget.Spent.Tokens != 4 || budget.Held.Tokens != 0 {
		t.Fatal(budget, err)
	}
	attempts, err := store.WorkflowAttempts(ctx, run.GroupID, run.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.Call.Node.Kind != "agent" {
			continue
		}
		trace, err := store.RunProviderObservation(ctx, run.GroupID, run.ID, attempt.Call.ID)
		if err != nil || len(trace.Outcome.Evidence) != 1 {
			t.Fatal("trace unavailable", trace, err)
		}
		if _, err = store.RunProviderObservation(ctx, "foreign", run.ID, attempt.Call.ID); !errors.Is(err, projections.ErrNotFound) {
			t.Fatal("foreign trace visible", err)
		}
		requestRaw, _ := json.Marshal(attempt.Call)
		ref := evolution.ArtifactRef{ID: "call-" + strings.TrimPrefix(evolution.Digest([]byte(run.ID+"\x00"+attempt.Call.ID)), "sha256:"), GroupID: run.GroupID, SchemaDigest: evolution.Digest([]byte("native-node-call-v1")), Digest: evolution.Digest(requestRaw), Size: int64(len(requestRaw)), MediaType: "application/json", GrantID: "run-grant", PolicyRevision: 1}
		key, _ := json.Marshal([]string{run.ID, attempt.Call.ID})
		call := projections.ProviderCall{ID: attempt.Call.ID, RunID: run.ID, Request: ref, IdempotencyKey: evolution.Digest(key)}
		outcome, err := provider.Lookup(ctx, call)
		if err != nil || len(outcome.Evidence) != 1 {
			t.Fatal(outcome, err)
		}
		raw, err := store.Artifact(ctx, outcome.Evidence[0])
		if err != nil {
			t.Fatal(err)
		}
		observation, err := evolution.Decode[ModelObservation](bytes.NewReader(raw))
		if err != nil || observation.ResponseModel != "model-build-2026" || observation.ToolTraceState != "complete" {
			t.Fatal(observation, err)
		}
	}
}

func modelCallFixture(t *testing.T, withTools ...bool) (*projections.Store, evolution.Revision, projections.ProviderCall, []byte) {
	t.Helper()
	store, _, run, executor, _ := nativeFixture(t)
	ctx := context.Background()
	var ref evolution.Revision
	for key := range executor.Registry.Models {
		ref = key
	}
	call := workflow.Call{ID: "call", Instance: "root/agent", Number: 1, Workflow: run.Pins.Workflow, Node: evolution.Node{ID: "agent", Kind: "agent", ModelRef: &ref}, Inputs: workflow.Values{"value": json.RawMessage(`"corpus"`)}, Prompt: "Read corpus"}
	if len(withTools) > 0 && withTools[0] {
		call.Node.Tools = []evolution.Revision{ref}
	}
	raw, _ := json.Marshal(call)
	artifact := evolution.ArtifactRef{ID: "request", GroupID: run.GroupID, SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(raw), Size: int64(len(raw)), MediaType: "application/json", GrantID: "run-grant", PolicyRevision: 1}
	if err := store.SealArtifact(ctx, artifact, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal([]string{run.ID, call.ID})
	return store, ref, projections.ProviderCall{ID: call.ID, RunID: run.ID, Request: artifact, IdempotencyKey: evolution.Digest(key)}, raw
}
func TestHTTPModelRejectsAmbiguousOrSubstitutedResponses(t *testing.T) {
	store, ref, call, _ := modelCallFixture(t)
	for _, mode := range []string{"pending", "missing", "gateway404", "wrong-model", "wrong-request", "bad-tool", "negative-usage", "duplicate", "oversize", "redirect", "denied"} {
		t.Run(mode, func(t *testing.T) {
			redirects := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects++; w.WriteHeader(200) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := ModelObservation{RunID: call.RunID, CallID: call.ID, IdempotencyKey: call.IdempotencyKey, RequestDigest: call.Request.Digest, Model: ref, ProviderRequestID: "provider", State: "completed", Outputs: workflow.Values{"value": json.RawMessage(`"output"`)}, ResponseModel: "pinned", ToolTraceState: "complete", Tools: []ModelToolObservation{}}
				switch mode {
				case "pending":
					value.State = "running"
				case "missing":
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"error":"model_call_not_found"}`))
					return
				case "gateway404":
					http.NotFound(w, r)
					return
				case "wrong-model":
					value.Model.Revision++
				case "wrong-request":
					value.RequestDigest = evolution.Digest([]byte("other"))
				case "bad-tool":
					value.Tools = []ModelToolObservation{{ID: "tool", Tool: ref, State: "succeeded", Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`)}}
				case "negative-usage":
					value.Usage.Tokens = -1
				case "duplicate":
					_, _ = w.Write([]byte(`{"state":"completed","state":"failed"}`))
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat("x", evolution.MaxDocumentBytes+1)))
					return
				case "redirect":
					http.Redirect(w, r, target.URL, 302)
					return
				case "denied":
					w.WriteHeader(403)
					return
				}
				_ = json.NewEncoder(w).Encode(value)
			}))
			defer server.Close()
			provider, err := NewHTTPModel(store, ModelConnection{Endpoint: server.URL, ClientID: "space", Secret: strings.Repeat("s", 32), Model: ref})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			_, err = provider.Lookup(context.Background(), call)
			want := ErrModelUnavailable
			switch mode {
			case "missing":
				want = projections.ErrProviderCallNotFound
			case "pending":
				want = ErrModelPending
			case "wrong-model", "wrong-request", "bad-tool", "denied":
				want = ErrModelDenied
			}
			if !errors.Is(err, want) || redirects != 0 {
				t.Fatal(mode, err, redirects)
			}
		})
	}
}
func TestHTTPModelCancellationRequiresConfirmedOutcome(t *testing.T) {
	store, ref, call, raw := modelCallFixture(t)
	ctx := context.Background()
	mode := "unknown"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "unknown" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"model_call_not_found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(ModelObservation{RunID: call.RunID, CallID: call.ID, IdempotencyKey: call.IdempotencyKey, RequestDigest: call.Request.Digest, Model: ref, ProviderRequestID: "canceled-request", State: "cancelled", Usage: projections.RunUsage{}, Outputs: workflow.Values{}, ResponseModel: "pinned", ToolTraceState: "omitted", Tools: []ModelToolObservation{}})
	}))
	defer server.Close()
	provider, err := NewHTTPModel(store, ModelConnection{Endpoint: server.URL, ClientID: "space", Secret: strings.Repeat("s", 32), Model: ref})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	lease, err := store.ClaimRun(ctx, call.RunID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReserveRunBudget(ctx, lease, call.ID, projections.RunUsage{Tokens: 10, CostMicros: 20}); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareProviderCall(ctx, lease, call.ID, call.Request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Submit(ctx, prepared, append(raw, ' '), projections.RunUsage{Tokens: 10}); !errors.Is(err, ErrModelDenied) {
		t.Fatal("changed request submitted", err)
	}
	if _, err = provider.Cancel(ctx, prepared); !errors.Is(err, ErrModelUnavailable) {
		t.Fatal("absence released uncertain effect", err)
	}
	mode = "confirmed"
	outcome, err := provider.Cancel(ctx, prepared)
	if err != nil || outcome.State != "cancelled" {
		t.Fatal(outcome, err)
	}
	if err = store.RecordProviderOutcome(ctx, lease, outcome); err != nil {
		t.Fatal(err)
	}
	budget, err := store.RunBudget(ctx, "group", call.RunID)
	if err != nil || budget.Held.Tokens != 0 {
		t.Fatal(budget, err)
	}
}

func TestHTTPModelRejectsUnsafeConnections(t *testing.T) {
	store, ref, _, _ := modelCallFixture(t)
	for _, origin := range []string{"http://remote.example", "http://localhost", "https://user:secret@example.com", "https://example.com/path", "https://example.com?", "https://example.com#fragment"} {
		if provider, err := NewHTTPModel(store, ModelConnection{Endpoint: origin, ClientID: "space", Secret: strings.Repeat("s", 32), Model: ref}); err == nil {
			provider.Close()
			t.Fatal("unsafe model origin", origin)
		}
	}
}

func TestHTTPModelProtectedRequestDeniesSubmitButPreservesCleanup(t *testing.T) {
	store, model, call, raw := modelCallFixture(t)
	ctx := context.Background()
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "protected-request", Revision: 1}, Dataset: call.Request, Scorer: model, Runtime: model, Model: model, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1, MinimumCoverage: 1}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	body, _ := json.Marshal(profile)
	if err := store.PublishDefinition(ctx, "profile", body); err != nil {
		t.Fatal(err)
	}
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == "GET" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"model_call_not_found"}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/cancel") {
			t.Error("protected prompt submitted")
		}
		input, _ := io.ReadAll(r.Body)
		if bytes.Contains(input, []byte("Read corpus")) {
			t.Error("cleanup transmitted protected prompt")
		}
		_ = json.NewEncoder(w).Encode(ModelObservation{RunID: call.RunID, CallID: call.ID, IdempotencyKey: call.IdempotencyKey, RequestDigest: call.Request.Digest, Model: model, ProviderRequestID: "provider", State: "cancelled", ToolTraceState: "complete"})
	}))
	defer server.Close()
	provider, err := NewHTTPModel(store, ModelConnection{Endpoint: server.URL, ClientID: "space", Secret: strings.Repeat("s", 32), Model: model})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	if _, err = provider.Submit(ctx, call, raw, projections.RunUsage{Tokens: 1}); !errors.Is(err, projections.ErrNotFound) {
		t.Fatal("protected submit accepted", err)
	}
	if _, err = provider.Lookup(ctx, call); !errors.Is(err, projections.ErrProviderCallNotFound) {
		t.Fatal("cleanup lookup blocked", err)
	}
	if outcome, err := provider.Cancel(ctx, call); err != nil || outcome.State != "cancelled" {
		t.Fatal("cleanup cancellation blocked", outcome, err)
	}
	if len(methods) != 2 || methods[0] != "GET" || methods[1] != "POST" {
		t.Fatal("unexpected provider effects", methods)
	}
}

func TestProtectedRunInputClosesRetainedInlineTrace(t *testing.T) {
	store, _, run, executor, _ := nativeFixture(t)
	ctx := context.Background()
	worker := Worker{Store: store, ID: "worker", Authority: executor.Authority, Executor: executor}
	if err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.WorkflowAttempts(ctx, run.GroupID, run.ID, "", 100)
	if err != nil || len(attempts) == 0 {
		t.Fatal("ordinary trace unavailable", err)
	}
	var condition evolution.Revision
	for ref := range executor.Registry.Models {
		condition = ref
	}
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "protected-input", Revision: 1}, Dataset: run.Pins.Inputs[0], Scorer: condition, Runtime: condition, Model: condition, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1, MinimumCoverage: 1}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	raw, _ := json.Marshal(profile)
	if err = store.PublishDefinition(ctx, "profile", raw); err != nil {
		t.Fatal(err)
	}
	if value, err := store.WorkflowAttempt(ctx, run.GroupID, run.ID, attempts[0].Call.ID); !errors.Is(err, projections.ErrNotFound) || len(value.Call.Inputs) != 0 {
		t.Fatal("protected inline attempt escaped", err)
	}
	if page, err := store.WorkflowAttemptPage(ctx, run.GroupID, run.ID, "", 100); !errors.Is(err, projections.ErrNotFound) || len(page.Items) != 0 {
		t.Fatal("protected attempt page escaped", err)
	}
	if records, err := store.CheckpointWorkflowAttempts(ctx, run.GroupID, run.ID, "node-"+attempts[0].Call.ID, "", 100); !errors.Is(err, projections.ErrNotFound) || len(records) != 0 {
		t.Fatal("protected checkpoint replay escaped", err)
	}
}
