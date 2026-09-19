package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/schemastate"
	"harness-platform/orchestrator/internal/store"
)

func TestCaptureCommandRequiresManagedSchemaAndCanonicalSingleton(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	control := filepath.Join(root, "control.db")
	memory := filepath.Join(root, "memory.db")
	st, err := store.Open(ctx, control)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mem, err := memorystore.Open(ctx, memory)
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/info" {
			t.Errorf("empty batch accessed engine data plane: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"store_id": "engine", "version": khora.EngineRevision, "manual_cold_recovery": true})
	}))
	defer engine.Close()
	args := []string{"--control-db", control, "--memory-db", memory, "--trajectory-root", filepath.Join(root, "archive"), "--engine-endpoint", engine.URL, "--once"}
	if err := run(ctx, args); err == nil {
		t.Fatal("unstamped databases were implicitly migrated")
	}
	if err := schemastate.Stamp(ctx, st.DBForTest()); err != nil {
		t.Fatal(err)
	}
	if err := schemastate.Stamp(ctx, mem.DBForTest()); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, args); err != nil {
		t.Fatal(err)
	}
	release, err := schemastate.LockPaths(control + ".native-capture")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	alias := filepath.Join(root, "alias.db")
	if err := os.Symlink(control, alias); err != nil {
		t.Fatal(err)
	}
	args[1] = alias
	if err := run(ctx, args); err == nil {
		t.Fatal("aliased database bypassed capture singleton")
	}
}
