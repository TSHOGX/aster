package main

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOfflinePublicationAndOwnership(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	service := evolution.Service{Ref: evolution.Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: evolution.Digest([]byte("runner")), Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	body, _ := json.Marshal(service)
	input, _ := json.Marshal(publication{Kind: "service", Ref: service.Ref, Body: body})
	var output bytes.Buffer
	if err := publish(ctx, dir, bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if err := publish(ctx, dir, bytes.NewReader(input), &output); err != nil {
		t.Fatal("retry", err)
	}
	store, err := projections.Open(ctx, filepath.Join(dir, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = publish(ctx, dir, bytes.NewReader(input), &output); err == nil {
		t.Fatal("live store ownership bypassed")
	}
	store.Close()
	wrong := service.Ref
	wrong.ID = "other"
	input, _ = json.Marshal(publication{Kind: "service", Ref: wrong, Body: body})
	if err = publish(ctx, dir, bytes.NewReader(input), &output); err == nil {
		t.Fatal("envelope substitution accepted")
	}
}
