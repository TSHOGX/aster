package khora

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

// writeEngine mirrors the measured engine's volume write surface, including the
// parts that are not idempotent on their own: mkdir answers 409 for an existing
// path, and a keyed snapshot replays to its original result.
type writeEngine struct {
	existing   map[string]string
	snapshots  map[string]string
	writes     map[string]int
	shortWrite bool
	mkdirCalls int
}

func newWriteEngine() *writeEngine {
	return &writeEngine{existing: map[string]string{}, snapshots: map[string]string{}, writes: map[string]int{}}
}

func (e *writeEngine) handler(t *testing.T, storeID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query := r.URL.Path, r.URL.Query()
		switch {
		case path == "/v1/info":
			json.NewEncoder(w).Encode(map[string]any{"store_id": storeID, "version": EngineRevision, "manual_cold_recovery": true})
		case strings.HasSuffix(path, "/mkdir"):
			e.mkdirCalls++
			name := query.Get("path")
			if kind, exists := e.existing[name]; exists {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte("mkdirat " + name + ": file exists (" + kind + ")"))
				return
			}
			e.existing[name] = "directory"
			json.NewEncoder(w).Encode(map[string]any{"name": name, "type": "directory", "is_dir": true, "mod_time": "2026-09-20T00:00:00Z"})
		case strings.HasSuffix(path, "/lstat"):
			name := query.Get("path")
			kind, exists := e.existing[name]
			if !exists {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"name": name, "type": kind, "is_dir": kind == "directory", "mod_time": "2026-09-20T00:00:00Z"})
		case strings.HasSuffix(path, "/write"):
			name := query.Get("path")
			body, _ := io.ReadAll(r.Body)
			e.writes[name]++
			e.existing[name] = "file"
			written := len(body)
			if e.shortWrite {
				written = written / 2
			}
			json.NewEncoder(w).Encode(map[string]any{"volume_id": "vol_source", "path": name, "bytes_written": written})
		case strings.HasSuffix(path, "/snapshot"):
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				t.Error("snapshot was dispatched without a durable key")
			}
			id, replay := e.snapshots[key]
			if !replay {
				id = "vol_derived"
				e.snapshots[key] = id
			}
			json.NewEncoder(w).Encode(map[string]any{"volume_id": id, "size": 1 << 20})
		case path == "/v1/volumes/vol_derived":
			json.NewEncoder(w).Encode(map[string]any{"volume_id": "vol_derived", "size": 1 << 20})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeBackend(t *testing.T, engine *writeEngine) (*Backend, sandbox.VolumeReference) {
	t.Helper()
	server := httptest.NewServer(engine.handler(t, "store"))
	t.Cleanup(server.Close)
	backend, err := New(context.Background(), Config{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return backend, sandbox.VolumeReference{StoreID: "store", VolumeID: "vol_source"}
}

// A resumed preparation replays the whole tree onto the same staging volume, so
// mkdir must tolerate a directory it already created.
func TestMkdirVolumeIsIdempotentForDirectories(t *testing.T) {
	engine := newWriteEngine()
	backend, reference := writeBackend(t, engine)
	ctx := context.Background()
	if err := backend.MkdirVolume(ctx, reference, "skills"); err != nil {
		t.Fatal(err)
	}
	if err := backend.MkdirVolume(ctx, reference, "skills"); err != nil {
		t.Fatalf("replayed mkdir was not idempotent: %v", err)
	}
	if engine.mkdirCalls != 2 {
		t.Fatalf("adapter did not dispatch both calls: %d", engine.mkdirCalls)
	}
	// An existing entry of the wrong type is still a conflict: the directory this
	// call promised does not exist.
	if err := backend.WriteVolume(ctx, reference, "AGENTS.md", []byte("context\n")); err != nil {
		t.Fatal(err)
	}
	if err := backend.MkdirVolume(ctx, reference, "AGENTS.md"); !sandbox.Is(err, sandbox.Conflict) {
		t.Fatalf("a file shadowing a directory was accepted: %v", err)
	}
}

// A short write leaves the path holding neither the old nor the intended bytes,
// because the engine replaces the target before streaming. That is an uncertain
// outcome for the caller to redo whole, not a partial success.
func TestWriteVolumeRejectsShortWrites(t *testing.T) {
	engine := newWriteEngine()
	engine.shortWrite = true
	backend, reference := writeBackend(t, engine)
	err := backend.WriteVolume(context.Background(), reference, "AGENTS.md", []byte("reviewed bytes\n"))
	if !sandbox.Is(err, sandbox.Uncertain) {
		t.Fatalf("short write was not reported uncertain: %v", err)
	}
}

func TestWriteVolumeBoundsAndValidates(t *testing.T) {
	engine := newWriteEngine()
	backend, reference := writeBackend(t, engine)
	ctx := context.Background()
	if err := backend.WriteVolume(ctx, reference, "AGENTS.md", make([]byte, maxWriteChunk+1)); !sandbox.Is(err, sandbox.Invalid) {
		t.Fatalf("unbounded write accepted: %v", err)
	}
	foreign := sandbox.VolumeReference{StoreID: "another-store", VolumeID: "vol_source"}
	if err := backend.WriteVolume(ctx, foreign, "AGENTS.md", []byte("x")); !sandbox.Is(err, sandbox.IdentityChanged) {
		t.Fatalf("write to a foreign engine store accepted: %v", err)
	}
	for _, name := range []string{"", "../escape", "a/../b", "/absolute"} {
		if err := backend.WriteVolume(ctx, reference, name, []byte("x")); !sandbox.Is(err, sandbox.Invalid) {
			t.Fatalf("unconfined path %q accepted: %v", name, err)
		}
		if err := backend.MkdirVolume(ctx, reference, name); !sandbox.Is(err, sandbox.Invalid) {
			t.Fatalf("unconfined directory %q accepted: %v", name, err)
		}
	}
}

// A keyed snapshot must replay to its original volume, and the adapter must
// inspect the result rather than trust the reply: a replayed key can name an
// object that was since deleted.
func TestSnapshotVolumeReplaysAndInspects(t *testing.T) {
	engine := newWriteEngine()
	backend, reference := writeBackend(t, engine)
	ctx := context.Background()
	first, err := backend.SnapshotVolume(ctx, reference, "content-publish:assets:digest", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.SnapshotVolume(ctx, reference, "content-publish:assets:digest", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if first.VolumeID != second.VolumeID || first.StoreID != "store" || first.Capacity != 1<<20 {
		t.Fatalf("keyed snapshot did not replay: %+v %+v", first, second)
	}
	if _, err := backend.SnapshotVolume(ctx, reference, "", 1<<20); !sandbox.Is(err, sandbox.Invalid) {
		t.Fatal("snapshot without a durable key accepted")
	}
	if _, err := backend.SnapshotVolume(ctx, reference, "key", 0); !sandbox.Is(err, sandbox.Invalid) {
		t.Fatal("snapshot without a capacity accepted")
	}
}

func TestVolumeAbsenceRetainsOnlyKnownOriginalIdentity(t *testing.T) {
	for _, operation := range []string{"create", "snapshot", "missing-source"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/info":
					json.NewEncoder(w).Encode(map[string]any{"store_id": "store", "version": EngineRevision, "manual_cold_recovery": true})
				case r.Method == "POST" && (r.URL.Path == "/v1/volumes" || strings.HasSuffix(r.URL.Path, "/snapshot")):
					if operation == "missing-source" {
						http.NotFound(w, r)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"volume_id": "original-volume", "size": 1 << 20})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			backend, err := New(context.Background(), Config{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			var result sandbox.Volume
			if operation == "create" {
				result, err = backend.CreateVolume(context.Background(), "original-key", 1<<20)
			} else {
				result, err = backend.SnapshotVolume(context.Background(), sandbox.VolumeReference{StoreID: "store", VolumeID: "source"}, "original-key", 1<<20)
			}
			if !sandbox.Is(err, sandbox.NotFound) {
				t.Fatal(err)
			}
			if operation == "missing-source" {
				if result.VolumeID != "" {
					t.Fatal("source failure invented allocation evidence", result)
				}
			} else if result.VolumeID != "original-volume" || result.StoreID != "store" {
				t.Fatal("original absence evidence lost", result)
			}
		})
	}
}
