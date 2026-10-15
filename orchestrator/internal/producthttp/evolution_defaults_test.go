package producthttp

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type defaultCatalog struct {
	browserCatalog
	template evolution.Template
}

func (c *defaultCatalog) CatalogDefinition(_ context.Context, kind string, ref evolution.Revision) (evaluationclient.CatalogEntry, error) {
	c.calls++
	if c.onRead != nil {
		c.onRead()
	}
	return evaluationclient.CatalogEntry{Ref: ref, Definition: c.template}, nil
}
func TestEvolutionTemplateDefaultsVerifyPinsRecheckAuthorityAndReplayOffline(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "defaults.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, "admin", "permanent-admin-password")
	must(t, err)
	ref := evolution.Revision{ID: "template", Revision: 1, Digest: evolution.Digest(nil)}
	template := evolution.Template{Ref: ref, Goal: "Example", Profile: "authored-v1", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: map[string]string{"input": ref.Digest}, Outputs: map[string]string{"output": ref.Digest}, Workflows: []evolution.Revision{ref}, SuggestedPrefix: "skills", Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
	template.Ref.Digest, err = evolution.DefinitionDigest(template)
	must(t, err)
	catalog := &defaultCatalog{template: template}
	auth := NewAuthentication(store, Config{})
	auth.EvolutionCatalog = catalog
	handler := auth.Handler(http.NotFoundHandler())
	input := product.EvolutionDraftInput{RequestID: "save", Settings: &evolution.DraftSettings{Bindings: map[string]evolution.BindingInheritance{"one": {Evaluation: true}, "two": {Evaluation: true}}}, Configuration: evolution.Configuration{GroupID: product.StandardUsersGroup, Bindings: []evolution.Binding{{ID: "one", Template: template.Ref, Parameters: json.RawMessage(`{}`)}, {ID: "two", Template: template.Ref, Parameters: json.RawMessage(`{}`)}}}}
	call := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest("PUT", "https://app/api/admin/groups/"+product.StandardUsersGroup+"/optimization/draft", strings.NewReader(string(raw)))
		r.Header.Set("Origin", "https://app")
		r.AddCookie(&http.Cookie{Name: loginCookie, Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if catalog.calls != 1 {
		t.Fatal("repeated immutable template read", catalog.calls)
	}
	auth.EvolutionCatalog = nil
	if w := call(); w.Code != 200 {
		t.Fatal("offline replay failed", w.Code, w.Body.String())
	}
	auth.EvolutionCatalog = catalog
	input.RequestID = "substituted"
	input.ExpectedRevision = 1
	catalog.template.Evaluation.Mode = "required"
	if w := call(); w.Code != 503 {
		t.Fatal("substituted default accepted", w.Code, w.Body.String())
	}
	catalog.template = template
	catalog.onRead = func() {
		_, err := store.SetEvolutionGroupGrant(ctx, token, product.StandardUsersGroup, product.EvolutionGroupGrantInput{RequestID: "revoke", AccountID: admin.ID, Action: "config.manage", Effect: "deny"})
		must(t, err)
	}
	if w := call(); w.Code != 403 {
		t.Fatal("late draft escaped revoke", w.Code, w.Body.String())
	}
	saved, err := store.EvolutionDraft(ctx, token, product.StandardUsersGroup)
	must(t, err)
	if saved.Revision != 1 || saved.Configuration.Bindings[0].Evaluation.Mode != "disabled" {
		t.Fatal(saved)
	}
}
