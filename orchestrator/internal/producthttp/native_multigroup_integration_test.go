//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/evolutionspaceclient"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/studioclient"
)

type nativeMixedGroups struct {
	group, connections string
	artifact, dataset  evolution.ArtifactRef
	evaluator          evolution.Service
	profile            evolution.EvaluationProfile
}

// Both groups share actual central, Studio and native runner processes. Only the
// model and evaluator transports are deterministic synthetic providers.
func provisionNativeMixedGroups(t *testing.T, ctx context.Context, store *product.Store, token, catalog, state string, artifact evolution.ArtifactRef, corpusFile, secret string) *nativeMixedGroups {
	t.Helper()
	name := "Beta"
	group, err := store.SaveGroup(ctx, token, "", product.GroupChange{RequestID: "beta", Name: &name})
	must(t, err)
	f := &nativeMixedGroups{group: group.Group.ID}
	f.artifact = artifact
	f.artifact.GroupID = f.group
	f.artifact.ID = "beta-corpus"
	f.artifact.GrantID = "beta-input"
	seal := func(ref evolution.ArtifactRef, file string) {
		raw, _ := json.Marshal(ref)
		cmd := exec.CommandContext(ctx, catalog, "--state-directory", state, "--action", "seal-artifact", "--artifact-file", file)
		cmd.Stdin = bytes.NewReader(raw)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seal mixed input: %v %s", err, out)
		}
	}
	seal(f.artifact, corpusFile)
	body := []byte(`{"expected":"verified"}`)
	file := filepath.Join(t.TempDir(), "held-out.json")
	must(t, os.WriteFile(file, body, 0600))
	f.dataset = evolution.ArtifactRef{ID: "beta-held-out", GroupID: f.group, GrantID: "beta-dataset", PolicyRevision: 1, SchemaDigest: evolution.Digest([]byte("dataset")), Digest: evolution.Digest(body), Size: int64(len(body)), MediaType: "application/json"}
	seal(f.dataset, file)
	f.evaluator = evolution.Service{Ref: evolution.Revision{ID: "mixed-evaluator", Revision: 1}, Kind: "evaluation", ConnectionID: "mixed-evaluator", RunnerDigest: evolution.Digest([]byte("mixed-evaluator")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	f.evaluator.Ref.Digest, err = evolution.DefinitionDigest(f.evaluator)
	must(t, err)
	ref := evolution.Revision{ID: "fixture", Revision: 1, Digest: evolution.Digest([]byte("fixture"))}
	f.profile = evolution.EvaluationProfile{Ref: evolution.Revision{ID: "beta-profile", Revision: 1}, Dataset: f.dataset, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1, MinimumCoverage: 1}}}
	f.profile.Ref.Digest, err = evolution.DefinitionDigest(f.profile)
	must(t, err)
	publishNativeInspectionDefinition(t, ctx, catalog, state, "service", f.evaluator.Ref, f.evaluator)
	publishNativeInspectionDefinition(t, ctx, catalog, state, "profile", f.profile.Ref, f.profile)
	var mu sync.Mutex
	jobs := map[string]evolution.EvaluationSubmission{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "space" || password != secret {
			w.WriteHeader(403)
			return
		}
		if r.Method == "PUT" {
			value, err := evolution.Decode[evolution.EvaluationSubmission](r.Body)
			if err != nil {
				w.WriteHeader(400)
				return
			}
			if value.Subject.Dataset != f.dataset {
				t.Error("foreign evaluation dataset")
				w.WriteHeader(403)
				return
			}
			jobs[value.JobID] = value
			_ = json.NewEncoder(w).Encode(evolution.JobStatus{JobID: value.JobID, Fence: value.Fence, State: "completed"})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 {
			w.WriteHeader(404)
			return
		}
		value, ok := jobs[parts[3]]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"evaluation_job_not_found"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/result") {
			result := evolution.EvaluationResult{JobID: value.JobID, Fence: value.Fence, Service: f.evaluator.Ref, Subject: value.Subject, State: "completed", Outcome: "pass", Metrics: []evolution.Metric{{Name: "accuracy", Unit: "ratio", Value: 1, Samples: 1, Coverage: 1}}}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "usage": map[string]int{"tokens": 1, "cost_micros": 1}})
			return
		}
		_ = json.NewEncoder(w).Encode(evolution.JobStatus{JobID: value.JobID, Fence: value.Fence, State: "completed"})
	}))
	t.Cleanup(provider.Close)
	raw, _ := json.Marshal(map[string]any{"connections": []any{map[string]string{"id": "mixed-evaluator", "endpoint": provider.URL, "client_id": "space", "secret_env": "TEST_EVALUATOR_SECRET", "input_secret_env": "TEST_EVALUATOR_INPUT_SECRET"}}})
	f.connections = filepath.Join(t.TempDir(), "connections.json")
	must(t, os.WriteFile(f.connections, raw, 0600))
	return f
}

func (f *nativeMixedGroups) start(t *testing.T, ctx context.Context, store *product.Store, token, actor, issuer string, alpha evolution.Configuration, space *evolutionspaceclient.Client, studio *studioclient.Client, call func(string, string, any) (int, []byte), private func(string, any) (int, []byte), state string) func() {
	t.Helper()
	request := func(method, path string, input, out any) {
		t.Helper()
		status, raw := call(method, path, input)
		if status < 200 || status >= 300 {
			t.Fatalf("mixed %s %s: %d %s", method, path, status, raw)
		}
		if out != nil {
			must(t, json.Unmarshal(raw, out))
		}
	}
	var library struct {
		ID string `json:"id"`
	}
	request("POST", "libraries", map[string]string{"request_id": "beta-library", "name": "Beta output", "profile": "shared-assets-v1"}, &library)
	for _, action := range []string{"volume.read", "volume.author", "volume.review", "volume.merge", "volume.configure"} {
		_, err := store.ApplyStudioGrant(ctx, token, product.StudioGrantCommand{RequestID: "beta-" + strings.ReplaceAll(action, ".", "-"), Kind: "account", SubjectID: actor, LibraryID: library.ID, Action: action, Effect: "allow"})
		must(t, err)
	}
	raw, _ := json.Marshal(alpha)
	var beta evolution.Configuration
	must(t, json.Unmarshal(raw, &beta))
	beta.GroupID = f.group
	for i := range beta.Sources {
		beta.Sources[i].AudienceGroups = []string{f.group}
		_, err := store.SetEvolutionSourcePolicy(ctx, token, f.group, product.EvolutionSourcePolicyInput{RequestID: "beta-source", Source: beta.Sources[i]})
		must(t, err)
	}
	for i := range beta.Bindings {
		b := &beta.Bindings[i]
		b.Output.LibraryID = library.ID
		b.Inputs = map[string]evolution.BindingInput{"corpus": {Artifact: &f.artifact}}
		if b.ID == "example-skills" {
			b.Evaluation = evolution.EvaluationPolicy{Mode: "required", ReferenceStrategy: "none", Service: &f.evaluator.Ref, Profile: &f.profile.Ref}
		}
	}
	pending, err := store.ActivateEvolutionConfig(ctx, token, beta.GroupID, product.EvolutionActivationInput{RequestID: "beta-activate", Configuration: beta})
	must(t, err)
	must(t, (&product.EvolutionWorker{Store: store, EvolutionSpace: space, Studio: studio}).Step(ctx))
	active, err := store.EvolutionConfig(ctx, token, beta.GroupID)
	must(t, err)
	if active.State != "active" {
		t.Fatal(active)
	}
	_, err = store.SetGenerationInputGrant(ctx, token, beta.GroupID, product.GenerationInputGrantInput{RequestID: "beta-input", Artifact: f.artifact, SourceIDs: []string{"source"}, Enabled: true})
	must(t, err)
	_, err = store.SetEvaluationDatasetGrant(ctx, token, beta.GroupID, product.EvaluationDatasetGrantInput{RequestID: "beta-dataset", Artifact: f.dataset, Service: f.evaluator.Ref, Enabled: true})
	must(t, err)
	// Membership and capability grants remain independent even with shared service
	// and template versions. An ordinary member never obtains the other history.
	for index, group := range []string{alpha.GroupID, beta.GroupID} {
		username := []string{"alpha-member", "beta-member"}[index]
		created, err := store.CreateAccount(ctx, token, product.AccountCreate{RequestID: username, Username: username, DisplayName: username, Role: "user", GroupIDs: &[]string{group}})
		must(t, err)
		_, temporary, err := store.Authenticate(ctx, username, created.TemporaryPassword)
		must(t, err)
		_, memberToken, err := store.ChangePassword(ctx, temporary, "", "permanent-member-password", true)
		must(t, err)
		_, err = store.SetEvolutionGroupGrant(ctx, token, group, product.EvolutionGroupGrantInput{RequestID: username + "-read", AccountID: created.User.ID, Action: "config.read", Effect: "allow"})
		must(t, err)
		_, err = store.EvolutionConfig(ctx, memberToken, group)
		must(t, err)
		foreign := beta.GroupID
		if index == 1 {
			foreign = alpha.GroupID
		}
		if _, err = store.EvolutionConfig(ctx, memberToken, foreign); !errors.Is(err, product.ErrForbidden) {
			t.Fatal("foreign group visible", err)
		}
	}
	root := "libraries/" + library.ID
	var base evolution.ContentRef
	request("GET", root+"/main", nil, &base)
	for _, b := range beta.Bindings {
		a := evolution.GenerationAssignment{RunID: "beta-" + b.ID, GroupID: beta.GroupID, BindingID: b.ID, OccurrenceID: "mixed", Requester: evolution.Principal{Issuer: issuer, Subject: actor, Kind: "human"}, Budget: b.Budget, Pins: evolution.RunPins{ConfigurationRevision: 1, ConfigurationDigest: pending.DesiredDigest, SourcePolicyRevision: 1, Template: b.Template, Workflow: b.Workflow, Service: b.Service, Inputs: []evolution.ArtifactRef{f.artifact}, InputPorts: map[string]evolution.ArtifactRef{"corpus": f.artifact}, Base: base, Output: b.Output, Evaluation: b.Evaluation}}
		status, raw := private("admit", a)
		if status != 200 {
			t.Fatal("mixed admission", status, string(raw))
		}
	}
	return func() {
		current := base
		for _, b := range beta.Bindings {
			var run struct{ State, Handoff, Branch string }
			for deadline := time.Now().Add(75 * time.Second); time.Now().Before(deadline); {
				status, raw := private("read", map[string]string{"group_id": beta.GroupID, "run_id": "beta-" + b.ID})
				if status != 200 {
					t.Fatal(status, string(raw))
				}
				must(t, json.Unmarshal(raw, &run))
				if run.Handoff == "submitted" {
					break
				}
				if run.State == "failed" {
					t.Fatal(string(raw))
				}
				time.Sleep(50 * time.Millisecond)
			}
			if run.Handoff != "submitted" {
				t.Fatal("Beta did not hand off", run)
			}
			if status, raw := private("read", map[string]string{"group_id": alpha.GroupID, "run_id": "beta-" + b.ID}); status == 200 {
				t.Fatal("run crossed group", string(raw))
			}
			var branch struct {
				Head    string
				Initial string `json:"initial_base"`
			}
			request("GET", root+"/branches/"+run.Branch, nil, &branch)
			if branch.Initial != base.CommitID {
				t.Fatal("base drifted")
			}
			var prepared evaluationProcessPrepared
			request("POST", root+"/integrations/prepare", map[string]any{"request_id": "beta-prepare-" + b.ID, "branch_id": run.Branch, "expected_head": branch.Head, "expected_main": current.CommitID, "expected_policy_revision": 1}, &prepared)
			expected := 0
			if b.Evaluation.Mode == "required" {
				expected = 1
			}
			if len(prepared.Identity.Checks) != expected {
				t.Fatal("group/path policy crossed", prepared.Identity.Checks)
			}
			if expected == 1 {
				status, _ := call("POST", root+"/integrations/reviews", map[string]any{"request_id": "beta-premature", "prepared_digest": prepared.Digest, "verdict": "approve"})
				if status != 409 {
					t.Fatal("required check bypassed", status)
				}
				var job struct{ ID string }
				request("POST", root+"/integrations/"+url.PathEscape(prepared.Digest)+"/evaluations", map[string]any{"request_id": "beta-evaluate", "requirement_digest": prepared.Identity.Checks[0].Digest, "budget": b.Budget, "seed": 0}, &job)
				settled := false
				for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
					var result struct{ State, Outcome string }
					request("POST", root+"/evaluations/"+job.ID+"/refresh", map[string]any{}, &result)
					if result.State == "completed" && result.Outcome == "pass" {
						settled = true
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				if !settled {
					status, raw := call("POST", root+"/evaluations/"+job.ID+"/refresh", map[string]any{})
					t.Fatal("mixed evaluation did not settle", status, string(raw))
				}
			}
			request("POST", root+"/integrations/reviews", map[string]any{"request_id": "beta-approve-" + b.ID, "prepared_digest": prepared.Digest, "verdict": "approve"}, nil)
			request("POST", root+"/integrations/merge", map[string]any{"request_id": "beta-merge-" + b.ID, "prepared_digest": prepared.Digest, "expected_main": current.CommitID}, nil)
			request("GET", root+"/main", nil, &current)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "projections.db")+"?mode=ro")
		must(t, err)
		defer db.Close()
		for _, group := range []evolution.Configuration{alpha, beta} {
			for _, b := range group.Bindings {
				id := b.ID
				if group.GroupID == beta.GroupID {
					id = "beta-" + id
				}
				rows, err := db.QueryContext(ctx, "SELECT record FROM workflow_attempts WHERE run_id=?", id)
				must(t, err)
				observed := 0
				for rows.Next() {
					var raw []byte
					must(t, rows.Scan(&raw))
					var attempt struct {
						Call struct {
							Prompt string
							Inputs map[string]json.RawMessage
						}
						Outputs map[string]json.RawMessage
						State   string
					}
					must(t, json.Unmarshal(raw, &attempt))
					if attempt.Call.Prompt != "" {
						observed++
						if len(attempt.Call.Inputs) == 0 || len(attempt.Outputs) == 0 || attempt.State != "succeeded" {
							t.Fatal("incomplete native trace", attempt)
						}
					}
				}
				must(t, rows.Err())
				rows.Close()
				if observed == 0 {
					t.Fatal("missing actual prompts", id)
				}
			}
		}
		unchanged, err := store.EvolutionConfig(ctx, token, alpha.GroupID)
		must(t, err)
		if unchanged.ActiveRevision != 1 {
			t.Fatal("Beta changed Alpha")
		}
	}
}
