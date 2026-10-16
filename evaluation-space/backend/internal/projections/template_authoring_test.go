package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOwnedTemplatePublicationIsolationRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	raw, err := os.ReadFile("../../../../contracts/evolution/testdata/skills.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = graph.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	workflow := evolution.Revision{ID: graph.ID, Revision: 1, Digest: evolution.Digest(raw)}
	if err = store.PublishWorkflow(ctx, workflow, raw); err != nil {
		t.Fatal(err)
	}
	template := evolution.Template{Ref: evolution.Revision{ID: "owned", Revision: 1}, Goal: "Extract skills", ParametersSchema: json.RawMessage(`{"type":"object"}`), Inputs: graph.Inputs, Outputs: graph.Outputs, Profile: "authored-v1", Workflows: []evolution.Revision{workflow}, SuggestedPrefix: "skills", Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}}
	encode := func(value evolution.Template) []byte {
		t.Helper()
		value.Ref.Digest, err = evolution.DefinitionDigest(value)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	invalid := template
	invalid.Workflows = []evolution.Revision{{ID: "missing", Revision: 1, Digest: workflow.Digest}}
	if err = store.PublishOwnedTemplate(ctx, "first", encode(invalid)); err == nil {
		t.Fatal("missing graph accepted")
	}
	// A rejected definition must not reserve its ID for the failed publisher.
	raw = encode(template)
	if err = store.PublishOwnedTemplate(ctx, "second", raw); err != nil {
		t.Fatal(err)
	}
	if err = store.PublishOwnedTemplate(ctx, "second", raw); err != nil {
		t.Fatal("replay", err)
	}
	if err = store.PublishOwnedTemplate(ctx, "first", raw); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign replay", err)
	}
	if err = store.PublishDefinition(ctx, "template", raw); !errors.Is(err, ErrConflict) {
		t.Fatal("offline takeover", err)
	}
	old := template
	_ = json.Unmarshal(raw, &old)
	template.Ref.Revision = 2
	template.Goal = "Revised skills"
	if err = store.PublishOwnedTemplate(ctx, "first", encode(template)); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign extension", err)
	}
	if err = store.PublishOwnedTemplate(ctx, "second", encode(template)); err != nil {
		t.Fatal(err)
	}
	template.Goal = "Conflicting revision"
	if err = store.PublishOwnedTemplate(ctx, "second", encode(template)); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrite", err)
	}
	template.Ref.Revision = 3
	template.Inputs = map[string]string{"wrong": workflow.Digest}
	if err = store.PublishOwnedTemplate(ctx, "second", encode(template)); err == nil {
		t.Fatal("mismatched ports accepted")
	}
	template = old
	template.Ref.ID = "operator"
	if err = store.PublishDefinition(ctx, "template", encode(template)); err != nil {
		t.Fatal(err)
	}
	if err = store.PublishOwnedTemplate(ctx, "second", encode(template)); !errors.Is(err, ErrConflict) {
		t.Fatal("operator ID claimed", err)
	}
	template.Ref.Revision = 2
	if err = store.PublishOwnedTemplate(ctx, "second", encode(template)); !errors.Is(err, ErrConflict) {
		t.Fatal("operator ID extended", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := store.CatalogDefinition(ctx, "template", old.Ref)
	if err != nil || !bytes.Equal(retained, encode(old)) {
		t.Fatal("old publication lost", err)
	}
	if err = store.PublishOwnedTemplate(ctx, "first", retained); !errors.Is(err, ErrConflict) {
		t.Fatal("reopened owner lost", err)
	}
	// Concurrent new publishers cannot both claim one ID.
	template = old
	template.Ref.ID = "contested"
	raw = encode(template)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, group := range []string{"first", "second"} {
		wg.Add(1)
		go func(group string) { defer wg.Done(); results <- store.PublishOwnedTemplate(ctx, group, raw) }(group)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}
