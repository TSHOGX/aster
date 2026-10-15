package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/examples"
	"aster/evaluation-space/internal/projections"
)

func TestExampleProvisioningIsExactReplayAndRejectsModelRebinding(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	ref := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	raw, _ := json.Marshal(map[string]any{"model": ref})
	var first, second bytes.Buffer
	if err := provisionExamples(ctx, directory, bytes.NewReader(raw), &first); err != nil {
		t.Fatal(err)
	}
	if err := provisionExamples(ctx, directory, bytes.NewReader(raw), &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("immutable example replay changed")
	}
	var packages []examples.Package
	if err := json.Unmarshal(first.Bytes(), &packages); err != nil || len(packages) != 2 {
		t.Fatal(err)
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		if _, err = store.CatalogDefinition(ctx, "template", pkg.Template.Ref); err != nil {
			t.Fatal(err)
		}
	}
	if err = provisionExamples(ctx, directory, bytes.NewReader(raw), &second); err == nil {
		t.Fatal("offline writer bypassed owner")
	}
	store.Close()
	ref.Revision = 2
	ref.Digest = evolution.Digest([]byte("other model"))
	raw, _ = json.Marshal(map[string]any{"model": ref})
	if err = provisionExamples(ctx, directory, bytes.NewReader(raw), &second); err == nil {
		t.Fatal("published graph rebound to new model")
	}
}
