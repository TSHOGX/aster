package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/examples"
	"aster/evolution-space/internal/projections"
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
	if err := json.Unmarshal(first.Bytes(), &packages); err != nil || len(packages) != 5 {
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

func TestBaselineProfileRequiresSealedGroupDatasetAndIsReplayable(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	dataset := []byte(`{"cases":[]}`)
	ref := evolution.ArtifactRef{ID: "facts-qa", GroupID: "group", SchemaDigest: evolution.Digest([]byte("qa-v1")), Digest: evolution.Digest(dataset), Size: int64(len(dataset)), MediaType: "application/json", GrantID: "dataset-grant", PolicyRevision: 1}
	pin := func(id string) evolution.Revision {
		return evolution.Revision{ID: id, Revision: 1, Digest: evolution.Digest([]byte(id))}
	}
	request, _ := json.Marshal(examples.BaselineInput{Template: "example-facts", Ref: evolution.Revision{ID: "facts-baseline", Revision: 1}, Dataset: ref, Scorer: pin("scorer"), Runtime: pin("runtime"), Model: pin("judge"), Tools: []evolution.Revision{}, FixedModelRequired: true})
	var out bytes.Buffer
	if err := provisionBaseline(ctx, directory, bytes.NewReader(request), &out); err == nil {
		t.Fatal("profile published before its dataset was sealed")
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SealArtifact(ctx, ref, bytes.NewReader(dataset)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	var first, second bytes.Buffer
	if err = provisionBaseline(ctx, directory, bytes.NewReader(request), &first); err != nil {
		t.Fatal(err)
	}
	if err = provisionBaseline(ctx, directory, bytes.NewReader(request), &second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("baseline replay changed", err)
	}
	profile, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(first.Bytes()))
	if err != nil || len(profile.Thresholds) != 3 || profile.Thresholds[2].Name != "answer_accuracy_delta" {
		t.Fatal(profile, err)
	}
	bad, _ := json.Marshal(examples.BaselineInput{Template: "unknown", Ref: evolution.Revision{ID: "x", Revision: 1}, Dataset: ref, Scorer: pin("scorer"), Runtime: pin("runtime"), Model: pin("judge"), Tools: []evolution.Revision{}})
	if err = provisionBaseline(ctx, directory, bytes.NewReader(bad), &out); err == nil {
		t.Fatal("unknown starter accepted")
	}
}
