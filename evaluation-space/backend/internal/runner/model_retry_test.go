package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/workflow"
)

func TestHTTPModelRetryHintRequiresKnownToolFreeFailure(t *testing.T) {
	for _, mode := range []string{"eligible", "omitted", "declared-tool", "observed-tool", "completed", "cancelled", "running"} {
		t.Run(mode, func(t *testing.T) {
			withTools := mode == "declared-tool" || mode == "observed-tool"
			store, ref, call, _ := modelCallFixture(t, withTools)
			observation := ModelObservation{RunID: call.RunID, CallID: call.ID, IdempotencyKey: call.IdempotencyKey, RequestDigest: call.Request.Digest, Model: ref, ProviderRequestID: "failed-call", State: "failed", Retryable: true, ToolTraceState: "complete"}
			switch mode {
			case "omitted":
				observation.ToolTraceState = "omitted"
			case "observed-tool":
				observation.Tools = []ModelToolObservation{{ID: "effect", Tool: ref, State: "succeeded", Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`)}}
			case "completed", "cancelled", "running":
				observation.State = mode
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(observation) }))
			defer server.Close()
			provider, err := NewHTTPModel(store, ModelConnection{Endpoint: server.URL, ClientID: "space", Secret: strings.Repeat("s", 32), Model: ref, Tools: []evolution.Revision{ref}})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			outcome, err := provider.Lookup(context.Background(), call)
			if observation.State != "failed" {
				if !errors.Is(err, ErrModelUnavailable) {
					t.Fatal("invalid retry hint accepted", outcome, err)
				}
				return
			}
			if err != nil || outcome.State != "failed" || outcome.Retryable != (mode == "eligible") {
				t.Fatal("incorrect retry eligibility or lost terminal accounting", outcome, err)
			}
			raw, err := store.Artifact(context.Background(), outcome.Evidence[0])
			var retained ModelObservation
			if err != nil || json.Unmarshal(raw, &retained) != nil || !retained.Retryable {
				t.Fatal("provider hint missing from trace", err)
			}
		})
	}
}

func TestNativeKnownFailureRetriesRemainBoundedAndAccounted(t *testing.T) {
	for _, mode := range []string{"lost-failure-reply", "exhausted", "tool-bearing", "omitted"} {
		t.Run(mode, func(t *testing.T) {
			store, _, run, executor, _ := nativeFixture(t, func(graph *evolution.Workflow) {
				graph.Nodes[0].MaxAttempts = 2
				if mode == "tool-bearing" {
					graph.Nodes[0].Tools = []evolution.Revision{*graph.Nodes[0].ModelRef}
				}
			})
			var ref evolution.Revision
			for model := range executor.Registry.Models {
				ref = model
			}
			var mu sync.Mutex
			retained := map[string]ModelObservation{}
			requests := []ModelSubmission{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				if prior, ok := retained[id]; ok {
					_ = json.NewEncoder(w).Encode(prior)
					return
				}
				if r.Method != "PUT" {
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"error":"model_call_not_found"}`))
					return
				}
				var request ModelSubmission
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				requests = append(requests, request)
				value := ModelObservation{RunID: request.RunID, CallID: request.CallID, IdempotencyKey: request.IdempotencyKey, RequestDigest: request.RequestDigest, Model: ref, ProviderRequestID: request.CallID, State: "failed", Retryable: true, Usage: projections.RunUsage{Tokens: 4, CostMicros: 7}, ToolTraceState: "complete"}
				if mode == "omitted" {
					value.ToolTraceState = "omitted"
				}
				if mode == "lost-failure-reply" && request.Call.Number == 2 {
					value.State, value.Retryable = "completed", false
					value.Outputs = workflow.Values{"value": json.RawMessage(`"model output"`)}
				}
				retained[id] = value
				if mode == "lost-failure-reply" && request.Call.Number == 1 {
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(value)
			}))
			defer server.Close()
			provider, err := NewHTTPModel(store, ModelConnection{Endpoint: server.URL, ClientID: "space", Secret: strings.Repeat("s", 32), Model: ref, Tools: []evolution.Revision{ref}})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			executor.Registry.Models[ref] = Model{Provider: provider, Ceiling: projections.RunUsage{Tokens: 10, CostMicros: 20}, Tools: map[evolution.Revision]bool{ref: true}}
			worker := Worker{Store: store, ID: "worker", Authority: executor.Authority, Executor: executor}
			if err = worker.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			state := "failed"
			if mode == "lost-failure-reply" {
				state = "completed"
			}
			current, err := store.Run(context.Background(), run.GroupID, run.ID)
			if err != nil || current.State != state {
				t.Fatal(current, err)
			}
			count := 2
			if mode == "tool-bearing" || mode == "omitted" {
				count = 1
			}
			mu.Lock()
			calls := append([]ModelSubmission{}, requests...)
			mu.Unlock()
			if len(calls) != count {
				t.Fatal("unexpected calls", len(calls), count)
			}
			if count == 2 && (calls[0].IdempotencyKey == calls[1].IdempotencyKey || calls[0].Call.Number != 1 || calls[1].Call.Number != 2 || calls[0].Call.Instance != calls[1].Call.Instance) {
				t.Fatal("retry lost numbered identity", calls)
			}
			budget, err := store.RunBudget(context.Background(), run.GroupID, run.ID)
			if err != nil || budget.Held.Tokens != 0 || budget.Spent.Tokens != int64(count*4) || budget.Spent.CostMicros != int64(count*7) {
				t.Fatal("retry cost changed", budget, err)
			}
			for _, call := range calls {
				trace, err := store.RunProviderObservation(context.Background(), run.GroupID, run.ID, call.CallID)
				if err != nil || trace.Outcome.Retryable != (count == 2 && (mode == "exhausted" || call.Call.Number == 1)) {
					t.Fatal("retry outcome not durable", trace, err)
				}
			}
			if err = worker.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			finalCount := len(requests)
			mu.Unlock()
			if finalCount != count {
				t.Fatal("terminal retry reexecuted", finalCount)
			}
		})
	}
}
