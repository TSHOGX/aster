package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

func TestLimitsCLITransmitsExactOperatorDeclaration(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "owner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	input := make(chan product.PlatformLimitEdit, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/limits" {
			t.Error(r.Method, r.URL)
		}
		var edit product.PlatformLimitEdit
		if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
			t.Error(err)
		}
		input <- edit
		w.Write([]byte(`{"revision":2}`))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	err = runLimits(context.Background(), []string{"set", "--socket", socket, "--dimension", "workspace_bytes", "--maximum", "9007199254740993", "--expected-revision", "1", "--request-id", "exact-cap"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-input; got.Maximum != 9007199254740993 || got.Dimension != product.ResourceWorkspace || got.ExpectedRevision != 1 || got.RequestID != "exact-cap" {
		t.Fatal(got)
	}
	for _, args := range [][]string{{"set", "--socket", socket, "--dimension", "workspace_bytes", "--expected-revision", "1", "--request-id", "missing-cap"}, {"set", "--socket", socket, "--dimension", "workspace_bytes", "--maximum", "01", "--expected-revision", "1", "--request-id", "ambiguous-cap"}} {
		if err := runLimits(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal("ambiguous cap accepted", args)
		}
	}
}
