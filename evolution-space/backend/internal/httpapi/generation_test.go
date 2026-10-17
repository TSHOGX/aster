package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/examples"
	"aster/evolution-space/internal/httpapi"
	"aster/evolution-space/internal/projections"
)

type generationCentral struct {
	calls      int
	denied     bool
	assignment evolution.GenerationAssignment
	grant      evolution.GenerationGrant
}

func (c *generationCentral) AdmitGeneration(_ context.Context, a evolution.GenerationAssignment) (evolution.GenerationGrant, error) {
	c.calls++
	if c.denied {
		return c.grant, errors.New("denied")
	}
	digest, _ := a.Digest()
	c.assignment = a
	c.grant = evolution.GenerationGrant{ID: "grant", GroupID: a.GroupID, RunID: a.RunID, AssignmentDigest: digest}
	return c.grant, nil
}
func (c *generationCentral) ResolveGeneration(_ context.Context, scope evolution.GenerationGrantScope) (evolution.GenerationAssignment, error) {
	c.calls++
	if c.denied || scope.Grant != c.grant {
		return c.assignment, errors.New("denied")
	}
	return c.assignment, nil
}
func TestPrivateGenerationAdmissionReplayAndScope(t *testing.T) {
	ctx := context.Background()
	store, err := projections.Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	model := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	packages, err := examples.Packages(model)
	if err != nil {
		t.Fatal(err)
	}
	pkg := packages[1]
	for _, published := range pkg.Workflows {
		raw, _ := published.Workflow.Canonical()
		if err = store.PublishWorkflow(ctx, published.Ref, raw); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(pkg.Template)
	if err = store.PublishDefinition(ctx, "template", raw); err != nil {
		t.Fatal(err)
	}
	service := evolution.Service{Ref: evolution.Revision{ID: "native", Revision: 1}, Kind: "evolution", Enabled: true, ConnectionID: "native", RunnerDigest: evolution.Digest([]byte("runner")), Capabilities: evolution.Capabilities{ProtocolVersion: 1, WorkflowVersions: []int{1}, NodeKinds: []string{"agent", "validator", "transform"}}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	raw, _ = json.Marshal(service)
	if err = store.PublishDefinition(ctx, "service", raw); err != nil {
		t.Fatal(err)
	}
	corpus := []byte(`{"episodes":[]}`)
	input := evolution.ArtifactRef{ID: "corpus", GroupID: "group", SchemaDigest: pkg.Template.Inputs["corpus"], Digest: evolution.Digest(corpus), Size: int64(len(corpus)), MediaType: "application/json", GrantID: "source-grant", PolicyRevision: 1}
	if err = store.SealArtifact(ctx, input, bytes.NewReader(corpus)); err != nil {
		t.Fatal(err)
	}
	budget := evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 10, TimeoutSeconds: 60, MaxArtifactBytes: 1 << 20}
	binding := evolution.Binding{ID: "corrections", Enabled: true, Template: pkg.Template.Ref, Workflow: pkg.Template.Workflows[0], Service: service.Ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"source"}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "lessons", Profile: "shared-assets-v1", ReservationGeneration: 1}, Evaluation: pkg.Template.Evaluation, Budget: budget, Trigger: "manual"}
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: budget, Bindings: []evolution.Binding{binding}, Sources: []evolution.SourceBinding{{ID: "source", Kind: "project", ResourceID: "project", PolicyRevision: 1, AudienceGroups: []string{"group"}, DerivativePublication: true}}}
	raw, _ = json.Marshal(config)
	digest := evolution.Digest(raw)
	if err = store.Adopt(ctx, projections.Projection{OperationID: "activation", Digest: digest, Configuration: config}); err != nil {
		t.Fatal(err)
	}
	assignment := evolution.GenerationAssignment{RunID: "run", GroupID: "group", BindingID: binding.ID, OccurrenceID: "manual", Requester: evolution.Principal{Issuer: "https://app", Subject: "operator", Kind: "human"}, Budget: budget, Pins: evolution.RunPins{ConfigurationRevision: 1, ConfigurationDigest: digest, SourcePolicyRevision: 1, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, InputPorts: map[string]evolution.ArtifactRef{"corpus": input}, Inputs: []evolution.ArtifactRef{input}, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "shared-assets-v1", TreeDigest: evolution.Digest([]byte("base"))}, Output: binding.Output, Evaluation: binding.Evaluation}}
	assignment.Budget.MaxTokens = 20
	assignment.Budget.TimeoutSeconds = 30
	central := &generationCentral{}
	secret := strings.Repeat("s", 32)
	api, err := httpapi.NewGenerationAPI(store, central, secret)
	if err != nil {
		t.Fatal(err)
	}
	call := func(action, user, password, origin string, input any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "/evolution-space/internal/v1/generation/"+action, bytes.NewReader(raw))
		r.SetBasicAuth(user, password)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		return w
	}
	for _, bad := range [][3]string{{"producer", secret, ""}, {"central", "wrong", ""}, {"central", secret, "https://app"}} {
		if w := call("admit", bad[0], bad[1], bad[2], assignment); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if central.calls != 0 {
		t.Fatal("caller borrowed central authority")
	}
	for range 2 {
		if w := call("admit", "central", secret, "", assignment); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	admitted, err := store.Run(ctx, "group", "run")
	if err != nil || admitted.Budget != assignment.Budget {
		t.Fatal("admission widened requested budget", admitted.Budget, err)
	}
	_, grant, err := store.GenerationGrant(ctx, "group", "run")
	if err != nil || grant != central.grant {
		t.Fatal(grant, err)
	}
	scope := map[string]any{"group_id": "group", "run_id": "run"}
	if w := call("read", "central", secret, "", scope); w.Code != 200 || strings.Contains(w.Body.String(), "episodes") {
		t.Fatal(w.Code, w.Body.String())
	}
	scope["group_id"] = "foreign"
	if w := call("read", "central", secret, "", scope); w.Code != 404 {
		t.Fatal("cross-group run visible", w.Code)
	}
	transfer := []byte(`{"episodes":[]}`)
	transferred := input
	transferred.ID = "transferred"
	reference, _ := json.Marshal(transferred)
	seal := func(data []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/evolution-space/internal/v1/generation/seal-input", bytes.NewReader(data))
		request.SetBasicAuth("central", secret)
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("X-Corpus-Artifact", base64.RawURLEncoding.EncodeToString(reference))
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		return response
	}
	if w := seal(transfer); w.Code != 200 {
		t.Fatal("online corpus transfer", w.Code, w.Body.String())
	}
	got, err := store.Artifact(ctx, transferred)
	if err != nil || !bytes.Equal(got, transfer) {
		t.Fatal("corpus changed", err)
	}
	if w := seal([]byte("wrong")); w.Code != 422 {
		t.Fatal("corrupt transfer accepted", w.Code)
	}
	t.Run("held-out-downloads", func(t *testing.T) { checkGenerationDatasetDownloads(t, store, input) })
	protectedAssignment := assignment
	protectedAssignment.RunID, protectedAssignment.OccurrenceID = "protected-run", "protected-occurrence"
	if w := call("admit", "central", secret, "", protectedAssignment); w.Code != 404 || strings.Contains(w.Body.String(), "episodes") {
		t.Fatal("held-out admission", w.Code, w.Body.String())
	}
	central.denied = true
	scope["group_id"] = "group"
	if w := call("read", "central", secret, "", scope); w.Code != 403 {
		t.Fatal("revoked read", w.Code)
	}
}

func (c *generationCentral) AuthorizeGenerationInput(context.Context, evolution.ArtifactRef) error {
	c.calls++
	if c.denied {
		return errors.New("denied")
	}
	return nil
}
