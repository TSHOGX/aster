//go:build integration

package producthttp

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/studioclient"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This test builds and launches the actual participant commands. Central identity,
// configuration state and reconciliation also use production handlers and stores.
func TestEvolutionRealParticipantProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../..")
	must(t, err)
	build := func(module, command, name string) string {
		binary := filepath.Join(t.TempDir(), name)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, command)
		cmd.Dir = filepath.Join(root, module)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
		return binary
	}
	catalogBinary := build("evaluation-space/backend", "./cmd/evaluation-catalog", "evaluation-catalog")
	studioBinary := build("volumes-studio/backend", "./cmd/studio", "studio")
	evaluationBinary := build("evaluation-space/backend", "./cmd/evaluation-space", "evaluation-space")
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "central.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	secret := strings.Repeat("s", 32)
	t.Setenv("STUDIO_IDENTITY_CLIENT_SECRET", secret)
	t.Setenv("STUDIO_CENTRAL_SECRET", secret)
	t.Setenv("EVALUATION_SPACE_IDENTITY_SECRET", secret)
	t.Setenv("EVALUATION_SPACE_CENTRAL_SECRET", secret)
	config := &identityconfig.Config{Issuer: "https://central", Listen: "127.0.0.1:0", Clients: []identityconfig.Client{
		{ID: "studio", Callback: "https://central/volumes-studio/auth/callback", SecretEnvs: []string{"STUDIO_IDENTITY_CLIENT_SECRET"}},
		{ID: "evaluation-space", Callback: "https://central/evaluation-space/auth/callback", SecretEnvs: []string{"EVALUATION_SPACE_IDENTITY_SECRET"}},
	}}
	must(t, config.CheckCredentials())
	central := httptest.NewServer(NewAuthentication(store, Config{Identity: config}).IdentityServiceHandler())
	defer central.Close()
	address := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		addr := listener.Addr().String()
		must(t, listener.Close())
		return addr
	}
	studioAddress, evaluationAddress := address(), address()
	start := func(binary, addr string, args ...string) {
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
			_ = logFile.Close()
		})
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			response, err := http.Get("http://" + addr + "/health-probe")
			if err == nil {
				response.Body.Close()
				return
			}
			select {
			case err := <-done:
				done <- err
				raw, _ := os.ReadFile(logFile.Name())
				t.Fatalf("service exited: %v\n%s", err, raw)
			default:
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("participant listener did not start")
	}
	start(studioBinary, studioAddress, "--listen", address(), "--participant-listen", studioAddress, "--state-directory", t.TempDir(), "--public-origin", "https://central", "--identity-endpoint", central.URL, "--publisher-id", "publisher")
	evaluationDirectory := t.TempDir()
	workflowRaw, err := os.ReadFile(filepath.Join(root, "contracts/evolution/testdata/skills.valid.json"))
	must(t, err)
	workflow, err := evolution.DecodeWorkflow(bytes.NewReader(workflowRaw))
	must(t, err)
	workflowRaw, err = workflow.Canonical()
	must(t, err)
	workflowRef := evolution.Revision{ID: workflow.ID, Revision: 1, Digest: evolution.Digest(workflowRaw)}
	provision := func(kind string, ref evolution.Revision, body []byte) {
		raw, err := json.Marshal(map[string]any{"kind": kind, "ref": ref, "body": json.RawMessage(body)})
		must(t, err)
		cmd := exec.CommandContext(ctx, catalogBinary, "--state-directory", evaluationDirectory)
		cmd.Stdin = bytes.NewReader(raw)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("catalog provision: %v\n%s", err, output)
		}
	}
	provision("workflow", workflowRef, workflowRaw)
	off := evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	template := evolution.Template{Ref: evolution.Revision{ID: "template", Revision: 1}, Goal: "Extract skills", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: workflow.Inputs, Outputs: workflow.Outputs, Profile: "shared-assets-v1", Workflows: []evolution.Revision{workflowRef}, SuggestedPrefix: "skills/derived", Evaluation: off}
	template.Ref.Digest, err = evolution.DefinitionDigest(template)
	must(t, err)
	raw, err := json.Marshal(template)
	must(t, err)
	provision("template", template.Ref, raw)
	kinds := []string{}
	seen := map[string]bool{}
	for _, node := range workflow.Nodes {
		if !seen[node.Kind] {
			kinds = append(kinds, node.Kind)
			seen[node.Kind] = true
		}
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: evolution.Digest([]byte("runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: kinds}}
	service.Ref.Digest, err = evolution.DefinitionDigest(service)
	must(t, err)
	raw, err = json.Marshal(service)
	must(t, err)
	provision("service", service.Ref, raw)
	start(evaluationBinary, evaluationAddress, "--listen", evaluationAddress, "--state-directory", evaluationDirectory, "--identity-endpoint", central.URL)
	catalogRequest, err := http.NewRequestWithContext(ctx, "GET", "http://"+evaluationAddress+"/evaluation-space/api/v1/catalog/service/service/1?digest="+service.Ref.Digest, nil)
	must(t, err)
	anonymous, err := http.DefaultClient.Do(catalogRequest)
	must(t, err)
	anonymous.Body.Close()
	if anonymous.StatusCode != 403 {
		t.Fatalf("catalog accepted anonymous caller: %d", anonymous.StatusCode)
	}
	catalogRequest.SetBasicAuth("central", secret)
	response, err := http.DefaultClient.Do(catalogRequest)
	must(t, err)
	var storedService evolution.Service
	err = json.NewDecoder(response.Body).Decode(&storedService)
	response.Body.Close()
	must(t, err)
	if response.StatusCode != 200 || storedService.Ref != service.Ref {
		t.Fatal("real catalog authority failed", response.StatusCode, storedService.Ref)
	}
	configuration := evolution.Configuration{GroupID: product.StandardUsersGroup, Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	_, err = store.CreateProject(ctx, admin.ID, "source-project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "source-project", PolicyRevision: 1, AudienceGroups: []string{configuration.GroupID}, DerivativePublication: true}
	_, err = store.SetEvolutionSourcePolicy(ctx, token, configuration.GroupID, product.EvolutionSourcePolicyInput{RequestID: "source-policy", Source: source})
	must(t, err)
	_, err = store.ApplyStudioGrant(ctx, token, product.StudioGrantCommand{RequestID: "configure", Kind: "account", SubjectID: admin.ID, LibraryID: "aster-shared-assets", Action: "volume.configure", Effect: "allow"})
	must(t, err)
	configuration.Sources = []evolution.SourceBinding{source}
	configuration.Bindings = []evolution.Binding{{ID: "skills", Template: template.Ref, Workflow: workflowRef, Service: service.Ref, Parameters: []byte(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "aster-shared-assets", Prefix: "skills/derived", Profile: "shared-assets-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Budget: configuration.Budget, Trigger: "manual"}}
	pending, err := store.ActivateEvolutionConfig(ctx, token, configuration.GroupID, product.EvolutionActivationInput{RequestID: "activate", Configuration: configuration})
	must(t, err)
	evaluation, err := evaluationclient.New("http://"+evaluationAddress, secret)
	must(t, err)
	studio, err := studioclient.New("http://"+studioAddress, secret)
	must(t, err)
	worker := &product.EvolutionWorker{Store: store, Evaluation: evaluation, Studio: studio}
	must(t, worker.Step(ctx))
	active, err := store.EvolutionConfig(ctx, token, configuration.GroupID)
	must(t, err)
	if active.State != "active" || active.ActiveDigest != pending.DesiredDigest || active.ActiveRevision != 1 {
		t.Fatal(active)
	}
	receipt, err := studio.Lookup(ctx, studioclient.Projection{OperationID: pending.OperationID, Digest: pending.DesiredDigest, Configuration: configuration})
	must(t, err)
	if receipt.State != "adopted" {
		t.Fatal(receipt)
	}
	configuration.Revision = 2
	configuration.Bindings[0].Output.ReservationGeneration = 2
	second, err := store.ActivateEvolutionConfig(ctx, token, configuration.GroupID, product.EvolutionActivationInput{RequestID: "update", ExpectedRevision: 1, Configuration: configuration})
	must(t, err)
	// Recreate the central worker to prove the next operation is discovered from
	// durable state rather than a retained interactive task.
	worker = &product.EvolutionWorker{Store: store, Evaluation: evaluation, Studio: studio}
	must(t, worker.Step(ctx))
	active, err = store.EvolutionConfig(ctx, token, configuration.GroupID)
	must(t, err)
	if active.State != "active" || active.ActiveRevision != 2 || active.ActiveDigest != second.DesiredDigest {
		t.Fatal("generation update", active)
	}

	configuration.Revision = 3
	configuration.Bindings[0].Output.ReservationGeneration = 3
	configuration.Bindings[0].Service.Revision = 99
	rejected, err := store.ActivateEvolutionConfig(ctx, token, configuration.GroupID, product.EvolutionActivationInput{RequestID: "invalid-service", ExpectedRevision: 2, Configuration: configuration})
	must(t, err)
	worker = &product.EvolutionWorker{Store: store, Evaluation: evaluation, Studio: studio}
	must(t, worker.Step(ctx))
	failed, err := store.EvolutionOperation(ctx, token, configuration.GroupID, rejected.OperationID)
	must(t, err)
	if failed.State != "failed" || len(failed.Participants) != 1 || failed.Participants[0].Error != "catalog_rejected" {
		t.Fatal("missing durable catalog rejection", failed)
	}
	must(t, store.DiscardEvolutionOperation(ctx, token, configuration.GroupID, rejected.OperationID, product.EvolutionRetryInput{RequestID: "discard-invalid", ExpectedRevision: 3}))
	worker = &product.EvolutionWorker{Store: store, Evaluation: evaluation, Studio: studio}
	must(t, worker.Step(ctx))
	discarded, err := store.EvolutionConfig(ctx, token, configuration.GroupID)
	must(t, err)
	if discarded.State != "discarded" || discarded.ActiveRevision != 2 || discarded.ActiveDigest != second.DesiredDigest {
		t.Fatal("discard changed accepted configuration", discarded)
	}
	// The cancelled candidate must not consume or replace the active generation.
	configuration.Revision = 4
	configuration.Bindings[0].Service = service.Ref
	replacement, err := store.ActivateEvolutionConfig(ctx, token, configuration.GroupID, product.EvolutionActivationInput{RequestID: "replacement", ExpectedRevision: 3, Configuration: configuration})
	must(t, err)
	worker = &product.EvolutionWorker{Store: store, Evaluation: evaluation, Studio: studio}
	must(t, worker.Step(ctx))
	active, err = store.EvolutionConfig(ctx, token, configuration.GroupID)
	must(t, err)
	if active.State != "active" || active.ActiveRevision != 4 || active.ActiveDigest != replacement.DesiredDigest {
		t.Fatal("replacement after discard failed", active)
	}

}
