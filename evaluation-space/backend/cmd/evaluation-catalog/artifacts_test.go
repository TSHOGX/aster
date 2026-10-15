package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

func TestOfflineArtifactSealingPreservesExactDatasetAndOwnership(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	source := filepath.Join(t.TempDir(), "dataset.json")
	data := []byte(`{"cases":[{"answer":"held-out"}]}`)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	ref := evolution.ArtifactRef{ID: "dataset", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(data), Size: int64(len(data)), MediaType: "application/json", GrantID: "dataset-use", PolicyRevision: 1}
	raw, _ := json.Marshal(ref)
	var output bytes.Buffer
	if err := sealArtifact(ctx, directory, source, bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	if err := sealArtifact(ctx, directory, source, bytes.NewReader(raw), &output); err != nil {
		t.Fatal("replay", err)
	}
	store, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Artifact(ctx, ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(string(got), err)
	}
	if err = sealArtifact(ctx, directory, source, bytes.NewReader(raw), &output); err == nil {
		t.Fatal("live owner bypassed")
	}
	store.Close()
	if err = os.WriteFile(source, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = sealArtifact(ctx, directory, source, bytes.NewReader(raw), &output); err == nil {
		t.Fatal("bytes rebound")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err = sealArtifact(ctx, directory, link, bytes.NewReader(raw), &output); err == nil {
		t.Fatal("symlink followed")
	}
}
