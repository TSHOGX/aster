package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestPrivateControlSocketUsesTheApplicationWriter(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := product.Open(ctx, filepath.Join(directory, "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &Application{Store: store, BrowserAddress: "127.0.0.1:8080"}
	socket := filepath.Join(directory, "control.sock")
	listener, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: app.controlHandler()}
	go server.Serve(listener)
	defer server.Close()
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if duplicate, err := listenControl(socket); err == nil {
		duplicate.Close()
		t.Fatal("live control socket was replaced")
	}
	var output bytes.Buffer
	if err = Control(ctx, socket, "POST", "/notify", map[string]string{"Audience": "alice", "Title": "Notice", "Body": "Internal fixture notice"}, &output); err != nil {
		t.Fatal(err)
	}
	notices, err := store.Notifications(ctx, "alice", "", 100)
	if err != nil || len(notices) != 1 {
		t.Fatal(notices, err)
	}
	other, err := store.Notifications(ctx, "bob", "", 100)
	if err != nil || len(other) != 0 {
		t.Fatal(other, err)
	}
	if err = Control(ctx, socket, "GET", "/status", nil, &output); err != nil || !bytes.Contains(output.Bytes(), []byte("occupied_projects")) {
		t.Fatal(output.String(), err)
	}
	output.Reset()
	if err = Control(ctx, socket, "POST", "/release/drain", map[string]any{"owner": "operator-test", "timeout_seconds": 60}, &output); err != nil {
		t.Fatal(err)
	}
	var release product.ReleaseState
	if err = json.Unmarshal(output.Bytes(), &release); err != nil || release.Phase != "draining" {
		t.Fatal(output.String(), err)
	}
	_, err = store.CreateProject(ctx, "alice", "p", "Project")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateSession(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, "s", "Session")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AdmitTurn(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, "s", "request", "work", false)
	if !errors.Is(err, product.ErrDraining) {
		t.Fatal(err)
	}
	output.Reset()
	if err = Control(ctx, socket, "GET", "/release", nil, &output); err != nil || !bytes.Contains(output.Bytes(), []byte("durable_blockers")) {
		t.Fatal(output.String(), err)
	}
	if err = Control(ctx, socket, "POST", "/release/abort", map[string]any{"owner": "stale", "generation": release.Generation}, &output); err == nil {
		t.Fatal("stale coordinator reopened admission")
	}
	output.Reset()
	if err = Control(ctx, socket, "POST", "/release/ready", map[string]any{"owner": "operator-test", "generation": release.Generation}, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"phase":"ready"`)) {
		t.Fatal(output.String())
	}
	output.Reset()
	if err = Control(ctx, socket, "POST", "/release/abort", map[string]any{"owner": "operator-test", "generation": release.Generation}, &output); err != nil {
		t.Fatal(err)
	}
	_, err = store.AdmitTurn(ctx, product.Scope{Actor: "alice", ProjectID: "p"}, "s", "request", "work", false)
	if err != nil {
		t.Fatal(err)
	}
}
