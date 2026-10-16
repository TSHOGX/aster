package main

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestOfflineArtifactExpiryRequiresStoppedOwnerAndRetainsReceipt(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s, err := projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	ref := evolution.ArtifactRef{ID: "retained", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("evidence")), Size: 8, MediaType: "text/plain", GrantID: "grant", PolicyRevision: 1}
	if err = s.SealArtifact(ctx, ref, bytes.NewBufferString("evidence")); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(projections.ArtifactExpirationInput{RequestID: "expire", Artifact: ref, Reason: "Retention selection"})
	var out bytes.Buffer
	if err = expireArtifact(ctx, directory, bytes.NewReader(request), &out); err == nil {
		t.Fatal("live service ownership bypassed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ref)
	if err = inspectArtifact(ctx, directory, bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var before projections.ArtifactRetention
	if err = json.Unmarshal(out.Bytes(), &before); err != nil || before.ExpiredAt != nil || len(before.Pins) != 0 {
		t.Fatal(before, err)
	}
	out.Reset()
	if err = expireArtifact(ctx, directory, bytes.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	receipt := append([]byte{}, out.Bytes()...)
	out.Reset()
	if err = expireArtifact(ctx, directory, bytes.NewReader(request), &out); err != nil || !bytes.Equal(receipt, out.Bytes()) {
		t.Fatal("exact retry changed receipt", err)
	}
	s, err = projections.Open(ctx, filepath.Join(directory, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if body, err := s.Artifact(ctx, ref); !errors.Is(err, projections.ErrArtifactExpired) || len(body) != 0 {
		t.Fatal("expired CLI bytes available", err)
	}
}
