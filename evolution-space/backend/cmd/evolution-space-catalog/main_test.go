package main

import (
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestOperatorCanInspectAndChangeServiceState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	service := evolution.Service{Ref: evolution.Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: evolution.Digest([]byte("runner")), Enabled: true, Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, _ = evolution.DefinitionDigest(service)
	body, _ := json.Marshal(service)
	raw, _ := json.Marshal(publication{Kind: "service", Ref: service.Ref, Body: body})
	var output bytes.Buffer
	if err := publish(ctx, dir, bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	inspect := func() projections.ServiceState {
		t.Helper()
		output.Reset()
		if err := inspectService(ctx, dir, strings.NewReader(`{"service_id":"service"}`), &output); err != nil {
			t.Fatal(err)
		}
		var state projections.ServiceState
		if err := json.Unmarshal(output.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	first := inspect()
	if first.Revision != 1 || !first.Enabled {
		t.Fatal(first)
	}
	request := projections.ServiceStateInput{RequestID: "disable", ServiceID: first.ServiceID, ExpectedRevision: first.Revision, Enabled: false}
	raw, _ = json.Marshal(request)
	output.Reset()
	if err := serviceState(ctx, dir, bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	second := inspect()
	if second.Revision != 2 || second.Enabled {
		t.Fatal(second)
	}
	if err := serviceState(ctx, dir, bytes.NewReader(raw), &output); err != nil {
		t.Fatal("retry", err)
	}
	if again := inspect(); again != second {
		t.Fatal("retry changed state", again)
	}
}
