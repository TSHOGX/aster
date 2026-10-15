//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/studioclient"
)

// Uses real Studio/Evaluation Space executables, central authorization/storage,
// offline dataset provisioning, and a deterministic evaluator that reads actual
// assigned input bytes before producing metrics. No store mutation bypasses a gate.
func TestRequiredEvaluationRealProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../..")
	must(t, err)
	build := func(module, command, name string) string {
		t.Helper()
		binary := filepath.Join(t.TempDir(), name)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, command)
		cmd.Dir = filepath.Join(root, module)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, raw)
		}
		return binary
	}
	catalog := build("evaluation-space/backend", "./cmd/evaluation-catalog", "catalog")
	studioBinary := build("volumes-studio/backend", "./cmd/studio", "studio")
	spaceBinary := build("evaluation-space/backend", "./cmd/evaluation-space", "space")
	address := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		value := listener.Addr().String()
		must(t, listener.Close())
		return value
	}
	studioAddress, participantAddress, spaceAddress := address(), address(), address()
	spaceOrigin := "http://" + spaceAddress
	var publicHandler http.Handler
	studioURL, _ := url.Parse("http://" + studioAddress)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { host := r.In.Host; r.SetURL(studioURL); r.Out.Host = host }}
	public := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/volumes-studio/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if publicHandler != nil {
			publicHandler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer public.Close()
	issuer := public.URL
	issuerURL, _ := url.Parse(issuer)

	secret := strings.Repeat("s", 32)
	inputSecret := strings.Repeat("i", 32)
	for _, name := range []string{"STUDIO_IDENTITY_CLIENT_SECRET", "STUDIO_CENTRAL_SECRET", "EVALUATION_SPACE_IDENTITY_SECRET", "EVALUATION_SPACE_CENTRAL_SECRET", "EVALUATION_SPACE_STUDIO_SECRET", "TEST_EVALUATOR_SECRET"} {
		t.Setenv(name, secret)
	}
	t.Setenv("TEST_EVALUATOR_INPUT_SECRET", inputSecret)
	t.Setenv("STUDIO_EVALUATION_INPUT_SECRET", inputSecret)
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "central.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	config := &identityconfig.Config{Issuer: issuer, Listen: "127.0.0.1:0", Clients: []identityconfig.Client{{ID: "studio", Callback: issuer + "/volumes-studio/auth/callback", SecretEnvs: []string{"STUDIO_IDENTITY_CLIENT_SECRET"}}, {ID: "evaluation-space", Callback: issuer + "/evaluation-space/auth/callback", SecretEnvs: []string{"EVALUATION_SPACE_IDENTITY_SECRET"}}}}
	must(t, config.CheckCredentials())
	authentication := NewAuthentication(store, Config{Identity: config})
	publicHandler = authentication.Handler(http.NotFoundHandler())
	central := httptest.NewServer(authentication.IdentityServiceHandler())
	defer central.Close()
	var providerMu sync.Mutex
	var submitted *evolution.EvaluationSubmission
	inputReads := 0
	providerFailure := ""
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerMu.Lock()
		defer providerMu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "space" || password != secret {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "PUT" {
			value, err := evolution.Decode[evolution.EvaluationSubmission](r.Body)
			if err != nil {
				providerFailure = err.Error()
				w.WriteHeader(400)
				return
			}
			if submitted != nil {
				if submitted.RequestID != value.RequestID {
					providerFailure = "second provider submission"
					w.WriteHeader(409)
					return
				}
			} else {
				read := func(kind, path string) ([]byte, error) {
					endpoint := spaceOrigin + "/evaluation-space/provider/v1/groups/" + value.Subject.Dataset.GroupID + "/jobs/" + value.JobID + "/" + kind
					if path != "" {
						endpoint += "?" + url.Values{"path": {path}}.Encode()
					}
					request, err := http.NewRequestWithContext(r.Context(), "GET", endpoint, nil)
					if err != nil {
						return nil, err
					}
					request.SetBasicAuth("evaluator", inputSecret)
					request.Header.Set("X-Evaluation-Grant", value.GrantID)
					request.Header.Set("X-Evaluation-Fence", fmt.Sprint(value.Fence))
					reply, err := http.DefaultClient.Do(request)
					if err != nil {
						return nil, err
					}
					defer reply.Body.Close()
					raw, err := io.ReadAll(reply.Body)
					if err != nil {
						return nil, err
					}
					if reply.StatusCode != 200 {
						return nil, fmt.Errorf("input %s: %d %s", kind, reply.StatusCode, raw)
					}
					inputReads++
					return raw, nil
				}
				dataset, err := read("dataset", "")
				if err != nil || string(dataset) != `{"expected":"Reviewed skill"}` {
					providerFailure = fmt.Sprint("dataset: ", err)
					w.WriteHeader(500)
					return
				}
				snapshot, err := read("candidate", "")
				if err != nil || !bytes.Contains(snapshot, []byte(value.Subject.Candidate.CommitID)) {
					providerFailure = fmt.Sprint("snapshot: ", err)
					w.WriteHeader(500)
					return
				}
				content, err := read("candidate", "skills/derived/SKILL.md")
				if err != nil || string(content) != "# Reviewed skill\nUse the verified example.\n" {
					providerFailure = fmt.Sprint("candidate: ", err)
					w.WriteHeader(500)
					return
				}
				submitted = &value
			}
			_ = json.NewEncoder(w).Encode(evolution.JobStatus{JobID: value.JobID, Fence: value.Fence, State: "completed"})
			return
		}
		if submitted == nil {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"evaluation_job_not_found"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/result") {
			service := evolution.Revision{ID: r.Header.Get("X-Evaluation-Service-ID"), Revision: 1, Digest: r.Header.Get("X-Evaluation-Service-Digest")}
			result := evolution.EvaluationResult{JobID: submitted.JobID, Fence: submitted.Fence, Service: service, Subject: submitted.Subject, State: "completed", Outcome: "fail", Metrics: []evolution.Metric{{Name: "accuracy", Unit: "ratio", Value: 1, Samples: 10, Coverage: 1}}}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "usage": map[string]int64{"tokens": 10, "cost_micros": 20}})
			return
		}
		_ = json.NewEncoder(w).Encode(evolution.JobStatus{JobID: submitted.JobID, Fence: submitted.Fence, State: "completed"})
	}))
	defer provider.Close()
	spaceDirectory := t.TempDir()
	provision := func(kind string, ref evolution.Revision, definition any) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind": kind, "ref": ref, "body": definition})
		cmd := exec.CommandContext(ctx, catalog, "--state-directory", spaceDirectory)
		cmd.Stdin = bytes.NewReader(raw)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("provision %s: %v %s", kind, err, output)
		}
	}
	workflowRaw, err := os.ReadFile(filepath.Join(root, "contracts/evolution/testdata/skills.valid.json"))
	must(t, err)
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(workflowRaw))
	must(t, err)
	canonical, err := workflow.Canonical()
	must(t, err)
	workflowRef := evolution.Revision{ID: workflow.ID, Revision: 1, Digest: evolution.Digest(canonical)}
	provision("workflow", workflowRef, json.RawMessage(canonical))
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	template := evolution.Template{Ref: evolution.Revision{ID: "template", Revision: 1}, Goal: "Skills", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: workflow.Inputs, Outputs: workflow.Outputs, Profile: "shared-assets-v1", Workflows: []evolution.Revision{workflowRef}, SuggestedPrefix: "skills/derived", Evaluation: off}
	template.Ref.Digest, err = evolution.DefinitionDigest(template)
	must(t, err)
	provision("template", template.Ref, template)
	kinds := []string{}
	seen := map[string]bool{}
	for _, node := range workflow.Nodes {
		if !seen[node.Kind] {
			seen[node.Kind] = true
			kinds = append(kinds, node.Kind)
		}
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "generation", Revision: 1}, Kind: "evolution", ConnectionID: "generation", RunnerDigest: evolution.Digest([]byte("runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: kinds}}
	service.Ref.Digest, err = evolution.DefinitionDigest(service)
	must(t, err)
	provision("service", service.Ref, service)
	evaluator := evolution.Service{Ref: evolution.Revision{ID: "evaluator", Revision: 1}, Kind: "evaluation", ConnectionID: "evaluator", RunnerDigest: evolution.Digest([]byte("evaluator")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	evaluator.Ref.Digest, err = evolution.DefinitionDigest(evaluator)
	must(t, err)
	provision("service", evaluator.Ref, evaluator)
	datasetBytes := []byte(`{"expected":"Reviewed skill"}`)
	datasetFile := filepath.Join(t.TempDir(), "dataset.json")
	must(t, os.WriteFile(datasetFile, datasetBytes, 0600))
	dataset := evolution.ArtifactRef{ID: "held-out", GroupID: product.StandardUsersGroup, SchemaDigest: evolution.Digest([]byte("dataset-schema")), Digest: evolution.Digest(datasetBytes), Size: int64(len(datasetBytes)), MediaType: "application/json", GrantID: "dataset-use", PolicyRevision: 1}
	raw, _ := json.Marshal(dataset)
	seal := exec.CommandContext(ctx, catalog, "--state-directory", spaceDirectory, "--action", "seal-artifact", "--artifact-file", datasetFile)
	seal.Stdin = bytes.NewReader(raw)
	if output, err := seal.CombinedOutput(); err != nil {
		t.Fatalf("seal: %v %s", err, output)
	}
	ref := evolution.Revision{ID: "condition", Revision: 1, Digest: evolution.Digest([]byte("condition"))}
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: "profile", Revision: 1}, Dataset: dataset, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 10, MinimumCoverage: 1}}}
	profile.Ref.Digest, err = evolution.DefinitionDigest(profile)
	must(t, err)
	provision("profile", profile.Ref, profile)
	connections := filepath.Join(t.TempDir(), "connections.json")
	raw, _ = json.Marshal(map[string]any{"connections": []any{map[string]string{"id": "evaluator", "endpoint": provider.URL, "client_id": "space", "secret_env": "TEST_EVALUATOR_SECRET", "input_secret_env": "TEST_EVALUATOR_INPUT_SECRET"}}})
	must(t, os.WriteFile(connections, raw, 0600))
	start := func(binary, addr string, args ...string) {
		t.Helper()
		logFile, err := os.Create(filepath.Join(t.TempDir(), "service.log"))
		must(t, err)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		must(t, cmd.Start())
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			logFile.Close()
		})
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			reply, err := http.Get("http://" + addr + "/probe")
			if err == nil {
				reply.Body.Close()
				return
			}
			select {
			case err := <-done:
				done <- err
				raw, _ := os.ReadFile(logFile.Name())
				t.Fatalf("service exit: %v %s", err, raw)
			default:
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("listener unavailable")
	}
	frontendAddress := address()
	browserEnabled := os.Getenv("STUDIO_TEST_EVALUATION_BROWSER") == "1"
	if browserEnabled {
		frontend := filepath.Join(root, "volumes-studio/frontend/.next/standalone/volumes-studio/frontend/server.js")
		cmd := exec.CommandContext(ctx, "node", frontend)
		cmd.Env = append(os.Environ(), "HOSTNAME=127.0.0.1", "PORT="+strings.Split(frontendAddress, ":")[1])
		cmd.Stdout = io.Discard
		cmd.Stderr = os.Stderr
		must(t, cmd.Start())
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
		// Production standalone requires static files alongside server.js.
		copyCmd := exec.CommandContext(ctx, "cp", "-r", filepath.Join(root, "volumes-studio/frontend/.next/static"), filepath.Join(root, "volumes-studio/frontend/.next/standalone/volumes-studio/frontend/.next/"))
		if output, err := copyCmd.CombinedOutput(); err != nil {
			t.Fatalf("static assets: %v %s", err, output)
		}
	}
	start(studioBinary, studioAddress, "--listen", studioAddress, "--participant-listen", participantAddress, "--state-directory", t.TempDir(), "--public-origin", issuer, "--identity-endpoint", central.URL, "--publisher-id", "publisher", "--evaluation-endpoint", spaceOrigin, "--frontend", "http://"+frontendAddress)
	start(spaceBinary, spaceAddress, "--listen", spaceAddress, "--state-directory", spaceDirectory, "--identity-endpoint", central.URL, "--evaluation-connections", connections, "--studio-input-endpoint", "http://"+participantAddress)
	for _, action := range []string{"volume.read", "volume.author", "volume.review", "volume.merge", "volume.configure"} {
		_, err = store.ApplyStudioGrant(ctx, token, product.StudioGrantCommand{RequestID: strings.ReplaceAll(action, ".", "-"), Kind: "account", SubjectID: admin.ID, LibraryID: "aster-shared-assets", Action: action, Effect: "allow"})
		must(t, err)
	}
	_, err = store.CreateProject(ctx, admin.ID, "source-project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "source-project", PolicyRevision: 1, AudienceGroups: []string{product.StandardUsersGroup}, DerivativePublication: true}
	_, err = store.SetEvolutionSourcePolicy(ctx, token, product.StandardUsersGroup, product.EvolutionSourcePolicyInput{RequestID: "source", Source: source})
	must(t, err)
	budget := evolution.Budget{MaxTokens: 1000, MaxCostMicros: 10000, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1048576}
	configuration := evolution.Configuration{GroupID: product.StandardUsersGroup, Revision: 1, Budget: budget, Sources: []evolution.SourceBinding{source}, Bindings: []evolution.Binding{{ID: "skills", Enabled: true, Template: template.Ref, Workflow: workflowRef, Service: service.Ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "aster-shared-assets", Prefix: "skills/derived", Profile: "shared-assets-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "required", Profile: &profile.Ref, Service: &evaluator.Ref, ReferenceStrategy: "none"}, Budget: budget, Trigger: "manual"}}}
	_, err = store.ActivateEvolutionConfig(ctx, token, configuration.GroupID, product.EvolutionActivationInput{RequestID: "activate", Configuration: configuration})
	must(t, err)
	evalClient, err := evaluationclient.New(spaceOrigin, secret)
	must(t, err)
	studioClient, err := studioclient.New("http://"+participantAddress, secret)
	must(t, err)
	must(t, (&product.EvolutionWorker{Store: store, Evaluation: evalClient, Studio: studioClient}).Step(ctx))
	_, err = store.SetEvaluationDatasetGrant(ctx, token, configuration.GroupID, product.EvaluationDatasetGrantInput{RequestID: "dataset", Artifact: dataset, Service: evaluator.Ref, Enabled: true})
	must(t, err)
	// Complete the actual Studio PKCE/session flow without a browser frontend.
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://"+studioAddress+"/volumes-studio/auth/login", nil)
	request.Host = issuerURL.Host
	login, err := httpClient.Do(request)
	must(t, err)
	login.Body.Close()
	location, err := url.Parse(login.Header.Get("Location"))
	must(t, err)
	code, err := store.AdmitIdentityCode(ctx, token, "studio", issuer+"/volumes-studio/auth/callback", location.Query().Get("code_challenge"))
	must(t, err)
	request, _ = http.NewRequestWithContext(ctx, "GET", "http://"+studioAddress+"/volumes-studio/auth/callback?"+url.Values{"state": {location.Query().Get("state")}, "code": {code}}.Encode(), nil)
	request.Host = issuerURL.Host
	for _, cookie := range login.Cookies() {
		request.AddCookie(cookie)
	}
	callback, err := httpClient.Do(request)
	must(t, err)
	callback.Body.Close()
	if callback.StatusCode != 303 {
		t.Fatal("Studio session failed", callback.StatusCode)
	}
	cookies := callback.Cookies()
	var csrf string
	call := func(method, path string, input any) (int, []byte) {
		t.Helper()
		var raw []byte
		if input != nil {
			raw, _ = json.Marshal(input)
		}
		request, _ := http.NewRequestWithContext(ctx, method, "http://"+studioAddress+"/volumes-studio/api/v1/"+path, bytes.NewReader(raw))
		request.Host = issuerURL.Host
		for _, cookie := range cookies {
			if cookie.MaxAge >= 0 {
				request.AddCookie(cookie)
			}
		}
		if method != "GET" {
			request.Header.Set("Origin", issuer)
			request.Header.Set("X-CSRF-Token", csrf)
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := httpClient.Do(request)
		must(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		must(t, err)
		return response.StatusCode, body
	}
	status, raw := call("GET", "session", nil)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	must(t, json.Unmarshal(raw, &session))
	csrf = session.CSRF
	libraryRoot := "libraries/aster-shared-assets"
	get := func(path string, out any) {
		t.Helper()
		status, raw := call("GET", libraryRoot+path, nil)
		if status != 200 {
			t.Fatal(status, string(raw))
		}
		must(t, json.Unmarshal(raw, out))
	}
	post := func(path string, input any, out any) {
		t.Helper()
		status, raw := call("POST", libraryRoot+path, input)
		if status < 200 || status >= 300 {
			t.Fatal(path, status, string(raw))
		}
		if out != nil {
			must(t, json.Unmarshal(raw, out))
		}
	}
	var main evolution.ContentRef
	get("/main", &main)
	var branch struct {
		ID   string `json:"id"`
		Head string `json:"head"`
	}
	post("/branches", map[string]any{"request_id": "branch", "name": "Evaluation candidate", "base_commit": main.CommitID}, &branch)
	post("/branches/"+branch.ID+"/draft/structure", map[string]any{"request_id": "directory", "expected_revision": 1, "expected_head": branch.Head, "action": "mkdir", "path": "skills/derived"}, nil)
	post("/branches/"+branch.ID+"/draft/text", map[string]any{"request_id": "skill", "expected_revision": 2, "expected_head": branch.Head, "path": "skills/derived/SKILL.md", "text": "# Reviewed skill\nUse the verified example.\n"}, nil)
	var checkpoint struct {
		Commit string `json:"commit_id"`
	}
	post("/branches/"+branch.ID+"/commits", map[string]any{"request_id": "commit", "expected_revision": 3, "expected_head": branch.Head, "message": "Reviewed skill"}, &checkpoint)
	var prepared struct {
		Digest   string                     `json:"digest"`
		Identity evolution.PreparedIdentity `json:"identity"`
	}
	post("/integrations/prepare", map[string]any{"request_id": "prepare", "branch_id": branch.ID, "expected_head": checkpoint.Commit, "expected_main": main.CommitID, "expected_policy_revision": 1}, &prepared)
	if len(prepared.Identity.Checks) != 1 {
		t.Fatal("required policy missing")
	}
	status, _ = call("POST", libraryRoot+"/integrations/reviews", map[string]any{"request_id": "early", "prepared_digest": prepared.Digest, "verdict": "approve", "rationale": "premature"})
	if status != 409 {
		t.Fatal("unmeasured integration approved", status)
	}
	if browserEnabled {
		width := 1440
		if os.Getenv("STUDIO_NARROW") == "1" {
			width = 390
		}
		browserName := os.Getenv("STUDIO_TEST_BROWSER")
		if browserName == "" {
			browserName = "chromium"
		}
		raw, _ := json.Marshal(map[string]any{"backend": "http://" + studioAddress, "origin": issuer, "cookies": cookies, "central_cookie": token, "branch": branch.ID, "prepared": prepared.Digest, "width": width, "browser": browserName})
		cmd := exec.CommandContext(ctx, "node", filepath.Join(root, "volumes-studio/tests/browser/evaluation.mjs"))
		cmd.Stdin = bytes.NewReader(raw)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("required evaluation browser: %v\n%s", err, output)
		}
		t.Log(string(output))
	} else {
		var job struct {
			ID string `json:"id"`
		}
		post("/integrations/"+url.PathEscape(prepared.Digest)+"/evaluations", map[string]any{"request_id": "evaluate", "requirement_digest": prepared.Identity.Checks[0].Digest, "budget": budget, "seed": 0}, &job)
		passed := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			status, raw = call("POST", libraryRoot+"/evaluations/"+job.ID+"/refresh", map[string]any{})
			if status != 200 {
				t.Fatal(status, string(raw))
			}
			var result struct {
				Outcome string `json:"outcome"`
			}
			must(t, json.Unmarshal(raw, &result))
			if result.Outcome == "pass" {
				passed = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		providerMu.Lock()
		reads, failure := inputReads, providerFailure
		providerMu.Unlock()
		if !passed || reads != 3 || failure != "" {
			t.Fatal("evaluation did not consume assigned inputs", passed, reads, failure)
		}
		post("/integrations/reviews", map[string]any{"request_id": "approve", "prepared_digest": prepared.Digest, "verdict": "approve", "rationale": "Exact evidence checked"}, nil)
		post("/integrations/merge", map[string]any{"request_id": "merge", "prepared_digest": prepared.Digest, "expected_main": main.CommitID}, nil)
	}
	providerMu.Lock()
	verifiedReads, providerError := inputReads, providerFailure
	providerMu.Unlock()
	if verifiedReads != 3 || providerError != "" {
		t.Fatal("provider did not read exact inputs", verifiedReads, providerError)
	}
	var accepted evolution.ContentRef
	get("/main", &accepted)
	if accepted.CommitID != prepared.Identity.Result.CommitID {
		t.Fatal("wrong integration accepted")
	}
}
