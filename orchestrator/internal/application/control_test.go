package application

import (
	"bytes"
	"context"
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
}
