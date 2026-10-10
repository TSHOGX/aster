package application

import (
	"bytes"
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/producttest"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestActivationFinishesControlResponseBeforeRetiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := producttest.Open(ctx, filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	drain, err := store.BeginReleaseDrain(ctx, "http-activation", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	if err != nil {
		t.Fatal(err)
	}
	app := &Application{Store: store, cancel: cancel}
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := listenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	app.controlServer = &http.Server{Handler: app.controlHandler()}
	defer app.controlServer.Close()
	go app.controlServer.Serve(listener)
	var output bytes.Buffer
	target := "sha256:" + strings.Repeat("a", 64)
	err = Control(context.Background(), socket, "POST", "/release/activate", map[string]any{"owner": drain.Owner, "generation": drain.Generation, "target": target}, &output)
	if err != nil {
		t.Fatal("activation response was lost", err)
	}
	var result product.ReleaseState
	if err = json.Unmarshal(output.Bytes(), &result); err != nil || result.Phase != "activating" {
		t.Fatal(err, output.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("owner retirement did not start")
	}
}
