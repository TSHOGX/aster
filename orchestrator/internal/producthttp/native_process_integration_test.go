//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

var errLostNativeAdmission = errors.New("lost native admission reply")

type lostNativeAdmission struct {
	client *evaluationclient.Client
	calls  int
}

func (l *lostNativeAdmission) SealGenerationInput(ctx context.Context, ref evolution.ArtifactRef, raw []byte) error {
	return l.client.SealGenerationInput(ctx, ref, raw)
}
func (l *lostNativeAdmission) AdmitGeneration(ctx context.Context, a evolution.GenerationAssignment) (evolution.Run, error) {
	result, err := l.client.AdmitGeneration(ctx, a)
	if err != nil {
		return result, err
	}
	l.calls++
	if l.calls == 1 {
		return evolution.Run{}, errLostNativeAdmission
	}
	return result, nil
}

func (l *lostNativeAdmission) GenerationOutput(ctx context.Context, scope evolution.GenerationOutputScope) (evolution.GenerationOutput, error) {
	return l.client.GenerationOutput(ctx, scope)
}

func TestNativeGenerationRealProcesses(t *testing.T) {
	for _, mode := range []string{"success", "sibling-failure", "lost-model-reply", "automatic", "dependencies", "dependency-failure", "backfill"} {
		t.Run(mode, func(t *testing.T) { nativeGenerationProcesses(t, mode) })
	}
}
func nativeGenerationProcesses(t *testing.T, mode string) {
	dependent := mode == "dependencies" || mode == "dependency-failure"
	automatic := mode == "automatic" || dependent
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	browserBackfill := mode == "backfill" && os.Getenv("ADMIN_EVOLUTION_BROWSER_TEST") == "1"
	var adminProxy http.Handler
	if browserBackfill {
		adminAddress := address()
		adminURL, _ := url.Parse("http://" + adminAddress)
		adminProxy = &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(adminURL) }}
		frontend := exec.CommandContext(ctx, "node", filepath.Join(root, "web/serve-standalone.mjs"), "admin-frontend", strings.Split(adminAddress, ":")[1])
		frontend.Stdout = io.Discard
		frontend.Stderr = os.Stderr
		must(t, frontend.Start())
		done := make(chan error, 1)
		go func() { done <- frontend.Wait() }()
		t.Cleanup(func() {
			_ = frontend.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = frontend.Process.Kill()
				<-done
			}
		})
		ready := false
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			response, err := http.Get(adminURL.String() + "/admin")
			if err == nil {
				response.Body.Close()
				ready = true
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		if !ready {
			t.Fatal("admin frontend unavailable")
		}
	}
	var publicHandler http.Handler
	studioURL, _ := url.Parse("http://" + studioAddress)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { host := r.In.Host; r.SetURL(studioURL); r.Out.Host = host }}
	public := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminProxy != nil && strings.HasPrefix(r.URL.Path, "/admin") {
			adminProxy.ServeHTTP(w, r)
			return
		}
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
	centralPath := filepath.Join(t.TempDir(), "central.db")
	store, err := product.Open(ctx, centralPath)
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

	modelRef := evolution.Revision{ID: "native-model", Revision: 1, Digest: evolution.Digest([]byte("native-model"))}
	var modelMu sync.Mutex
	responses := map[string]json.RawMessage{}
	calls := map[string]int{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelMu.Lock()
		defer modelMu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "space" || password != secret {
			w.WriteHeader(403)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 6 {
			w.WriteHeader(404)
			return
		}
		runID, callID := parts[3], parts[5]
		key := runID + "/" + callID
		if retained, ok := responses[key]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(retained)
			return
		}
		if r.Method != "PUT" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"model_call_not_found"}`))
			return
		}
		var submission struct {
			RunID  string             `json:"run_id"`
			CallID string             `json:"call_id"`
			Key    string             `json:"idempotency_key"`
			Digest string             `json:"request_digest"`
			Model  evolution.Revision `json:"model"`
			Call   struct {
				Node   evolution.Node             `json:"node"`
				Inputs map[string]json.RawMessage `json:"inputs"`
				Prompt string                     `json:"prompt"`
			} `json:"call"`
		}
		if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if submission.Model != modelRef || submission.Call.Prompt == "" {
			t.Error("model pin or effective prompt missing")
		}
		calls[runID]++
		proposal := map[string]any{"id": "verified", "title": "Verified task", "body": "Perform the task and verify the output.", "evidence_ids": []string{"episode"}, "status": "supported"}
		var outputs any
		switch submission.Call.Node.ID {
		case "integrate":
			if !bytes.Contains(submission.Call.Inputs["upstream"], []byte("Evidence IDs: episode")) {
				t.Error("dependent workflow did not receive actual upstream artifact")
			}
			outputs = map[string]any{"candidate": map[string]any{"files": []any{map[string]any{"path": "verified.md", "text": "Reviewed upstream skill. Evidence IDs: episode"}}, "held": []any{}}}
		case "extract":
			outputs = map[string]any{"items": []any{proposal}}
		case "draft":
			outputs = map[string]any{"state": map[string]any{"corpus": submission.Call.Inputs["corpus"], "items": []any{proposal}, "valid": false}}
		case "critic":
			var state map[string]any
			if err := json.Unmarshal(submission.Call.Inputs["state"], &state); err != nil {
				t.Error(err)
			}
			state["valid"] = true
			outputs = map[string]any{"state": state}
		default:
			t.Error("unexpected node", submission.Call.Node.ID)
			w.WriteHeader(400)
			return
		}
		raw, _ := json.Marshal(map[string]any{"run_id": submission.RunID, "call_id": submission.CallID, "idempotency_key": submission.Key, "request_digest": submission.Digest, "model": modelRef, "provider_request_id": key, "state": "completed", "usage": map[string]int{"tokens": 4, "cost_micros": 7}, "outputs": outputs, "response_model": "native-fixture-v1", "tool_trace_state": "complete", "tools": []any{}})
		if mode == "sibling-failure" && runID == "example-corrections" || mode == "dependency-failure" && submission.Call.Node.ID == "draft" {
			var value map[string]any
			must(t, json.Unmarshal(raw, &value))
			value["state"] = "failed"
			value["outputs"] = map[string]any{}
			raw, _ = json.Marshal(value)
		}
		responses[key] = raw
		if mode == "lost-model-reply" && runID == "example-skills" && calls[runID] == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer model.Close()
	spaceDirectory := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"model": modelRef})
	provision := exec.CommandContext(ctx, catalog, "--state-directory", spaceDirectory, "--action", "examples")
	provision.Stdin = bytes.NewReader(raw)
	output, err := provision.CombinedOutput()
	if err != nil {
		t.Fatalf("examples: %v %s", err, output)
	}
	var packages []struct {
		Template  evolution.Template `json:"template"`
		Workflows []struct {
			Ref      evolution.Revision `json:"ref"`
			Workflow evolution.Workflow `json:"workflow"`
		} `json:"workflows"`
		Schemas map[string]json.RawMessage `json:"schemas"`
	}
	must(t, json.Unmarshal(output, &packages))
	var dependentTemplate evolution.Template
	if dependent {
		schema := packages[0].Template.Outputs["candidate"]
		prompt := "Review this successful upstream candidate: {{upstream}}"
		graph := evolution.Workflow{SchemaVersion: 1, ID: "dependent-review", ParametersSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Inputs: map[string]string{"upstream": schema}, Outputs: map[string]string{"candidate": schema}, Nodes: []evolution.Node{{ID: "integrate", Kind: "agent", InputPorts: map[string]string{"upstream": schema}, OutputPorts: map[string]string{"candidate": schema}, ModelRef: &modelRef, Prompt: &evolution.Prompt{Text: prompt, Variables: []string{"upstream"}, Digest: evolution.Digest([]byte(prompt))}, MaxAttempts: 1, TimeoutSeconds: 30}}, Edges: []evolution.Edge{{From: "$input.upstream", To: "integrate.upstream"}, {From: "integrate.candidate", To: "$output.candidate"}}, Limits: evolution.Limits{MaxParallelNodes: 1, MaxTotalAttempts: 2, TimeoutSeconds: 60}}
		graphRaw, err := graph.Canonical()
		must(t, err)
		ref := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(graphRaw)}
		publish := func(kind string, ref evolution.Revision, body any) {
			raw, _ := json.Marshal(map[string]any{"kind": kind, "ref": ref, "body": body})
			cmd := exec.CommandContext(ctx, catalog, "--state-directory", spaceDirectory)
			cmd.Stdin = bytes.NewReader(raw)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("dependency publication: %v %s", err, output)
			}
		}
		publish("workflow", ref, graph)
		dependentTemplate = evolution.Template{Ref: evolution.Revision{ID: "dependent-review", Revision: 1}, Goal: "Review upstream skill", ParametersSchema: graph.ParametersSchema, Inputs: graph.Inputs, Outputs: graph.Outputs, Profile: "shared-assets-v1", Workflows: []evolution.Revision{ref}, SuggestedPrefix: "reviewed", Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
		dependentTemplate.Ref.Digest, err = evolution.DefinitionDigest(dependentTemplate)
		must(t, err)
		publish("template", dependentTemplate.Ref, dependentTemplate)
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "native-service", Revision: 1}, Kind: "evolution", ConnectionID: "native", RunnerDigest: evolution.Digest([]byte("native-runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: []string{"agent", "validator", "transform", "loop"}}}
	service.Ref.Digest, err = evolution.DefinitionDigest(service)
	must(t, err)
	raw, _ = json.Marshal(map[string]any{"kind": "service", "ref": service.Ref, "body": service})
	provision = exec.CommandContext(ctx, catalog, "--state-directory", spaceDirectory)
	provision.Stdin = bytes.NewReader(raw)
	if output, err = provision.CombinedOutput(); err != nil {
		t.Fatalf("service: %v %s", err, output)
	}
	corpusBytes := []byte(`{"episodes":[{"id":"episode","family_id":"family","family_kind":"declared","outcome":"completed","truncated":false,"text":"The task completed with verified output."}]}`)
	corpusFile := filepath.Join(t.TempDir(), "corpus.json")
	must(t, os.WriteFile(corpusFile, corpusBytes, 0600))
	artifact := evolution.ArtifactRef{ID: "corpus", GroupID: product.StandardUsersGroup, SchemaDigest: packages[0].Template.Inputs["corpus"], Digest: evolution.Digest(corpusBytes), Size: int64(len(corpusBytes)), MediaType: "application/json", GrantID: "corpus-use", PolicyRevision: 1}
	raw, _ = json.Marshal(artifact)
	provision = exec.CommandContext(ctx, catalog, "--state-directory", spaceDirectory, "--action", "seal-artifact", "--artifact-file", corpusFile)
	provision.Stdin = bytes.NewReader(raw)
	if output, err = provision.CombinedOutput(); err != nil {
		t.Fatalf("corpus: %v %s", err, output)
	}
	nativePath := filepath.Join(t.TempDir(), "native.json")
	raw, _ = json.Marshal(map[string]any{"models": []any{map[string]any{"reference": modelRef, "endpoint": model.URL, "client_id": "space", "secret_env": "TEST_EVALUATOR_SECRET", "ceiling": map[string]int{"tokens": 100, "cost_micros": 1000}}}, "schemas": map[string]any{}, "example_builders": true})
	must(t, os.WriteFile(nativePath, raw, 0600))
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

	start(studioBinary, studioAddress, "--listen", studioAddress, "--participant-listen", participantAddress, "--state-directory", t.TempDir(), "--public-origin", issuer, "--identity-endpoint", central.URL, "--publisher-id", "publisher", "--evaluation-endpoint", spaceOrigin)
	start(spaceBinary, spaceAddress, "--listen", spaceAddress, "--state-directory", spaceDirectory, "--identity-endpoint", central.URL, "--native-config", nativePath, "--studio-input-endpoint", "http://"+participantAddress)
	for _, action := range []string{"volume.read", "volume.author", "volume.review", "volume.merge", "volume.configure"} {
		_, err = store.ApplyStudioGrant(ctx, token, product.StudioGrantCommand{RequestID: strings.ReplaceAll(action, ".", "-"), Kind: "account", SubjectID: admin.ID, LibraryID: "aster-shared-assets", Action: action, Effect: "allow"})
		must(t, err)
	}
	_, err = store.CreateProject(ctx, admin.ID, "project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{product.StandardUsersGroup}, DerivativePublication: true}
	_, err = store.SetEvolutionSourcePolicy(ctx, token, product.StandardUsersGroup, product.EvolutionSourcePolicyInput{RequestID: "source", Source: source})
	must(t, err)
	budget := evolution.Budget{MaxTokens: 1000, MaxCostMicros: 10000, MaxAttempts: 20, TimeoutSeconds: 120, MaxArtifactBytes: 1 << 20}
	groupBudget := budget
	groupBudget.MaxTokens *= 2
	groupBudget.MaxCostMicros *= 2
	configuration := evolution.Configuration{GroupID: product.StandardUsersGroup, Revision: 1, Budget: groupBudget, Sources: []evolution.SourceBinding{source}}
	for _, pkg := range packages {
		configuration.Bindings = append(configuration.Bindings, evolution.Binding{ID: pkg.Template.Ref.ID, Enabled: true, Template: pkg.Template.Ref, Workflow: pkg.Template.Workflows[0], Service: service.Ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "aster-shared-assets", Prefix: pkg.Template.SuggestedPrefix, Profile: "shared-assets-v1", ReservationGeneration: 1}, Evaluation: pkg.Template.Evaluation, Budget: budget, Trigger: "manual"})
	}
	if mode == "backfill" {
		for i := range configuration.Bindings {
			configuration.Bindings[i].Inputs = map[string]evolution.BindingInput{"corpus": {Sources: []string{"source"}}}
		}
	}
	if automatic {
		configuration.AutoEnabled = true
		for i := range configuration.Bindings {
			configuration.Bindings[i].Trigger = "schedule"
			configuration.Bindings[i].Schedule = "@every 1m"
			configuration.Bindings[i].Timezone = "Asia/Shanghai"
			configuration.Bindings[i].Inputs = map[string]evolution.BindingInput{"corpus": {Artifact: &artifact}}
		}
	}
	if dependent {
		configuration.Bindings = append(configuration.Bindings, evolution.Binding{ID: dependentTemplate.Ref.ID, Enabled: true, Template: dependentTemplate.Ref, Workflow: dependentTemplate.Workflows[0], Service: service.Ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "aster-shared-assets", Prefix: "reviewed", Profile: "shared-assets-v1", ReservationGeneration: 1}, Evaluation: dependentTemplate.Evaluation, Budget: budget, Trigger: "schedule", Schedule: "@every 1m", Timezone: "Asia/Shanghai", DependsOn: []string{"example-skills"}, Inputs: map[string]evolution.BindingInput{"upstream": {Dependency: &evolution.BindingDependency{BindingID: "example-skills", OutputPort: "candidate"}}}})
	}
	pending, err := store.ActivateEvolutionConfig(ctx, token, configuration.GroupID, product.EvolutionActivationInput{RequestID: "activate", Configuration: configuration})
	must(t, err)
	evalClient, err := evaluationclient.New(spaceOrigin, secret)
	must(t, err)
	studioClient, err := studioclient.New("http://"+participantAddress, secret)
	must(t, err)
	must(t, (&product.EvolutionWorker{Store: store, Evaluation: evalClient, Studio: studioClient}).Step(ctx))
	active, err := store.EvolutionConfig(ctx, token, configuration.GroupID)
	must(t, err)
	if active.State != "active" {
		t.Fatal(active)
	}
	_, err = store.SetGenerationInputGrant(ctx, token, configuration.GroupID, product.GenerationInputGrantInput{RequestID: "input", Artifact: artifact, SourceIDs: []string{"source"}, Enabled: true})
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

	privateCall := func(action string, input any) (int, []byte) {
		t.Helper()
		raw, _ := json.Marshal(input)
		request, _ := http.NewRequestWithContext(ctx, "POST", spaceOrigin+"/evaluation-space/internal/v1/generation/"+action, bytes.NewReader(raw))
		request.SetBasicAuth("central", secret)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		must(t, err)
		defer response.Body.Close()
		raw, err = io.ReadAll(response.Body)
		must(t, err)
		return response.StatusCode, raw
	}
	runIDs := map[string]string{}
	skipped := map[string]bool{}
	if mode == "backfill" {
		// Synthetic retained source episode; capture, transfer, admission, models,
		// handoff and human review use the actual running services below.
		stamp := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z07:00")
		native := `{"type":"custom","customType":"aster.execution","data":{"run_id":"episode","phase":"start"}}` + "\n" + `{"type":"message","message":{"role":"user","content":"Perform and verify the task."}}` + "\n" + `{"type":"message","message":{"role":"assistant","content":"The task completed with verified output."}}` + "\n" + `{"type":"custom","customType":"aster.execution","data":{"run_id":"episode","phase":"end"}}` + "\n"
		hostPath := filepath.Join(t.TempDir(), "episode.jsonl")
		must(t, os.WriteFile(hostPath, []byte(native), 0600))
		db, err := sql.Open("sqlite", centralPath)
		must(t, err)
		db.SetMaxOpenConns(1)
		_, err = db.Exec("PRAGMA foreign_keys=ON")
		must(t, err)
		tx, err := db.BeginTx(ctx, nil)
		must(t, err)
		_, err = tx.Exec(`INSERT INTO sessions(id,project_id,title,creation_digest,created_at,updated_at) VALUES('source-session','project','Source','fixture',?,?)`, stamp, stamp)
		must(t, err)
		_, err = tx.Exec(`INSERT INTO turns(id,project_id,session_id,request_id,content,initially_held,disposition,sequence,created_at,updated_at) VALUES('family','project','source-session','fixture','Perform the task',0,'handled',1,?,?)`, stamp, stamp)
		must(t, err)
		_, err = tx.Exec(`INSERT INTO agent_incarnations(id,project_id,session_id,store_id,sandbox_id,epoch,handle,owner,state,created_at) VALUES('source-incarnation','project','source-session','fixture-store','fixture-sandbox',1,'fixture-handle',1,'exited',?)`, stamp)
		must(t, err)
		_, err = tx.Exec(`INSERT INTO execution_runs(id,project_id,session_id,prompt_id,incarnation_id,owner,state,acknowledged,outcome,started_at,settled_at) VALUES('episode','project','source-session','family','source-incarnation',1,'settled',1,'completed',?,?)`, stamp, stamp)
		must(t, err)
		_, err = tx.Exec(`INSERT INTO native_captures(turn_id,incarnation_id,path,digest,size,host_path,state) VALUES('family','source-incarnation','/runtime/sessions/source-session/native/captures/episode.jsonl',?,?,?,'exported')`, evolution.Digest([]byte(native)), len(native), hostPath)
		must(t, err)
		must(t, tx.Commit())
		must(t, db.Close())
		if browserBackfill {
			resultPath := filepath.Join(t.TempDir(), "backfill-preview.json")
			cmd := exec.CommandContext(ctx, "node", "node_modules/@playwright/test/cli.js", "test", "--config", "playwright.evolution.config.ts")
			cmd.Dir = filepath.Join(root, "admin-frontend")
			cmd.Env = append(os.Environ(), "ADMIN_EVOLUTION_URL="+issuer, "ADMIN_EVOLUTION_TOKEN="+token, "ADMIN_EVOLUTION_RESULT="+resultPath)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("backfill browser: %v\n%s", err, output)
			}
			raw, err := os.ReadFile(resultPath)
			must(t, err)
			var preview product.EvolutionBackfill
			must(t, json.Unmarshal(raw, &preview))
			if preview.State != "queued" || len(preview.Runs) != 2 {
				t.Fatal("browser did not queue exact preview", preview)
			}
		} else {
			_, err = store.SetEvolutionGroupGrant(ctx, token, configuration.GroupID, product.EvolutionGroupGrantInput{RequestID: "backfill-source-read", AccountID: admin.ID, Action: "source.read", Effect: "allow"})
			must(t, err)
			budgets := map[string]evolution.Budget{}
			for _, b := range configuration.Bindings {
				limited := b.Budget
				limited.MaxTokens = 200
				limited.MaxCostMicros = 2000
				budgets[b.ID] = limited
			}
			preview, err := store.PreviewEvolutionBackfill(ctx, token, configuration.GroupID, product.EvolutionBackfillInput{RequestID: "preview", ConfigurationRevision: 1, From: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano), Until: time.Now().UTC().Format(time.RFC3339Nano), MaxEpisodes: 1, Budgets: budgets})
			must(t, err)
			if len(preview.Episodes) != 1 || preview.Episodes[0].Truncated {
				t.Fatal("backfill fixture did not capture complete evidence", preview)
			}
			_, err = store.CommandEvolutionBackfill(ctx, token, configuration.GroupID, preview.ID, "execute", product.BackfillCommand{RequestID: "execute", Digest: preview.Digest})
			must(t, err)

		}
		transport := &lostNativeAdmission{client: evalClient}
		dispatcher := &product.EvolutionDispatcher{Store: store, Evaluation: transport, Studio: studioClient, Issuer: issuer}
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			if err := dispatcher.Step(ctx); errors.Is(err, errLostNativeAdmission) {
				dispatcher = &product.EvolutionDispatcher{Store: store, Evaluation: transport, Studio: studioClient, Issuer: issuer}
			} else {
				must(t, err)
			}
			page, err := store.EvolutionTriggers(ctx, token, configuration.GroupID, 0)
			must(t, err)
			for _, item := range page.Items {
				if item.DispatchState == "dispatched" {
					runIDs[item.BindingID] = item.RunID
				}
			}
			if len(runIDs) == len(configuration.Bindings) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(runIDs) != 2 || transport.calls != 3 {
			t.Fatal("manual backfill replay", runIDs, transport.calls)
		}
	} else if automatic {
		triggers := &product.EvolutionTriggerWorker{Store: store}
		transport := &lostNativeAdmission{client: evalClient}
		dispatcher := &product.EvolutionDispatcher{Store: store, Evaluation: transport, Studio: studioClient, Issuer: issuer}
		for deadline := time.Now().Add(80 * time.Second); time.Now().Before(deadline); {
			must(t, triggers.Step(ctx))
			if err := dispatcher.Step(ctx); errors.Is(err, errLostNativeAdmission) {
				dispatcher = &product.EvolutionDispatcher{Store: store, Evaluation: transport, Studio: studioClient, Issuer: issuer}
			} else {
				must(t, err)
			}
			page, err := store.EvolutionTriggers(ctx, token, configuration.GroupID, 0)
			must(t, err)
			for _, item := range page.Items {
				if item.DispatchState == "skipped" {
					skipped[item.BindingID] = true
					runIDs[item.BindingID] = item.RunID
				}
				if item.DispatchState == "dispatched" {
					runIDs[item.BindingID] = item.RunID
				}
			}
			if len(runIDs) == len(configuration.Bindings) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if transport.calls != len(configuration.Bindings)-len(skipped)+1 {
			t.Fatal("admission response loss did not replay exactly", transport.calls)
		}
		if len(runIDs) != len(configuration.Bindings) {
			t.Fatal("automatic schedule not dispatched", runIDs)
		}
	} else {
		for _, binding := range configuration.Bindings {
			runIDs[binding.ID] = binding.ID
			pins := evolution.RunPins{ConfigurationRevision: 1, ConfigurationDigest: pending.DesiredDigest, SourcePolicyRevision: 1, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, InputPorts: map[string]evolution.ArtifactRef{"corpus": artifact}, Inputs: []evolution.ArtifactRef{artifact}, Base: main, Output: binding.Output, Evaluation: binding.Evaluation}
			assignment := evolution.GenerationAssignment{RunID: binding.ID, GroupID: configuration.GroupID, BindingID: binding.ID, OccurrenceID: "manual", Requester: evolution.Principal{Issuer: issuer, Subject: admin.ID, Kind: "human"}, Pins: pins, Budget: budget}
			assignment.Budget.MaxTokens = 200
			assignment.Budget.MaxCostMicros = 2000
			status, raw := privateCall("admit", assignment)
			if status != 200 {
				t.Fatal("admission", binding.ID, status, string(raw))
			}
			var admitted evolution.Run
			must(t, json.Unmarshal(raw, &admitted))
			if admitted.Budget != assignment.Budget {
				t.Fatal("native admission widened explicit budget", admitted.Budget)
			}

		}

	}
	branches := map[string]string{}
	failed := skipped
	for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); {
		for _, binding := range configuration.Bindings {
			if branches[binding.ID] != "" || failed[binding.ID] {
				continue
			}
			status, raw := privateCall("read", map[string]string{"group_id": configuration.GroupID, "run_id": runIDs[binding.ID]})
			if status != 200 {
				t.Fatal("status", status, string(raw))
			}
			var state struct {
				State   string `json:"state"`
				Handoff string `json:"handoff"`
				Branch  string `json:"branch"`
			}
			must(t, json.Unmarshal(raw, &state))
			if state.State == "failed" {
				if mode == "sibling-failure" && binding.ID == "example-corrections" || mode == "dependency-failure" && binding.ID == "example-skills" {
					failed[binding.ID] = true
					continue
				}
				t.Fatal("native execution failed", binding.ID, string(raw))
			}
			if state.Handoff == "submitted" {
				branches[binding.ID] = state.Branch
			}
		}
		if len(branches)+len(failed) == len(configuration.Bindings) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(branches)+len(failed) != len(configuration.Bindings) {
		t.Fatal("native handoff not settled", branches)
	}
	var unchanged evolution.ContentRef
	get("/main", &unchanged)
	if unchanged != main {
		t.Fatal("machine changed protected main")
	}
	for _, binding := range configuration.Bindings {
		if failed[binding.ID] {
			continue
		}
		var branch struct {
			ID      string `json:"id"`
			Head    string `json:"head"`
			Initial string `json:"initial_base"`
		}
		get("/branches/"+branches[binding.ID], &branch)
		if branch.Initial != main.CommitID {
			t.Fatal("different generation base")
		}
		path := binding.Output.Prefix + "/verified.md"
		if binding.Output.Prefix == "skills" {
			path = "skills/verified/SKILL.md"
		}
		status, raw := call("GET", libraryRoot+"/branches/"+branch.ID+"/head-file?"+url.Values{"head": {branch.Head}, "path": {path}}.Encode(), nil)
		if status != 200 || !bytes.Contains(raw, []byte("Evidence IDs: episode")) {
			t.Fatal("generated file absent", status, string(raw))
		}
		var prepared struct {
			Digest string `json:"digest"`
		}
		post("/integrations/prepare", map[string]any{"request_id": "prepare-" + binding.ID, "branch_id": branch.ID, "expected_head": branch.Head, "expected_main": unchanged.CommitID, "expected_policy_revision": 1}, &prepared)
		post("/integrations/reviews", map[string]any{"request_id": "approve-" + binding.ID, "prepared_digest": prepared.Digest, "verdict": "approve", "rationale": "Verified generated files"}, nil)
		post("/integrations/merge", map[string]any{"request_id": "merge-" + binding.ID, "prepared_digest": prepared.Digest, "expected_main": unchanged.CommitID}, nil)
		get("/main", &unchanged)
	}
	modelMu.Lock()
	skillCalls, correctionCalls := calls[runIDs["example-skills"]], calls[runIDs["example-corrections"]]
	dependentCalls := calls[runIDs["dependent-review"]]
	modelMu.Unlock()
	expectedSkills := 2
	if mode == "dependency-failure" {
		expectedSkills = 1
	}
	if dependent {
		expected := 1
		if mode == "dependency-failure" {
			expected = 0
		}
		if dependentCalls != expected {
			t.Fatal("dependent model calls", dependentCalls)
		}
	}
	if skillCalls != expectedSkills || correctionCalls != 1 {
		t.Fatal("independent graph model calls", skillCalls, correctionCalls)
	}
}
