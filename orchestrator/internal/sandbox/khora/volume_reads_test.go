package khora

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/sandbox"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVolumeReadAdapterPinsIdentityAndBoundsRanges(t *testing.T) {
	storeID := "store"
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/info":
			json.NewEncoder(w).Encode(map[string]any{"store_id": storeID, "version": EngineRevision, "manual_cold_recovery": true})
		case "/v1/volumes/volume/lstat":
			json.NewEncoder(w).Encode(map[string]any{"name": "file", "type": "file", "size": 6, "mod_time": "2026-09-19T00:00:00Z"})
		case "/v1/volumes/volume/list":
			if r.URL.Query().Get("path") != "." {
				t.Error("volume root listing changed")
			}
			json.NewEncoder(w).Encode([]map[string]any{{"name": "file", "type": "file", "size": 6, "mod_time": "2026-09-19T00:00:00Z"}})
		case "/v1/volumes/volume/read":
			reads++
			if r.URL.Query().Get("offset") != "1" || r.URL.Query().Get("length") != "2" {
				t.Error("volume range changed")
			}
			w.Write([]byte("bc"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	backend, err := New(ctx, Config{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	reference := sandbox.VolumeReference{StoreID: "store", VolumeID: "volume"}
	if entries, err := backend.ListVolume(ctx, reference, ""); err != nil || len(entries) != 1 || entries[0].Name != "file" {
		t.Fatalf("volume directory: %+v %v", entries, err)
	}
	entry, err := backend.LstatVolume(ctx, reference, "file")
	if err != nil || entry.Type != "file" || entry.Size != 6 {
		t.Fatalf("portable metadata: %+v %v", entry, err)
	}
	data, err := backend.ReadVolume(ctx, reference, "file", 1, 2)
	if err != nil || string(data) != "bc" {
		t.Fatalf("range: %q %v", data, err)
	}
	for _, length := range []int64{0, -1, 256<<10 + 1} {
		if _, err := backend.ReadVolume(ctx, reference, "file", 1, length); !sandbox.Is(err, sandbox.Invalid) {
			t.Fatalf("invalid range accepted: %d %v", length, err)
		}
	}
	for _, name := range []string{"../file", " parent/file", "parent /file"} {
		if _, err := backend.ReadVolume(ctx, reference, name, 1, 2); !sandbox.Is(err, sandbox.Invalid) {
			t.Fatalf("ambiguous path reached engine: %q %v", name, err)
		}
	}
	storeID = "replaced"
	if _, err := backend.ReadVolume(ctx, reference, "file", 1, 2); !sandbox.Is(err, sandbox.IdentityChanged) {
		t.Fatalf("replaced engine identity accepted: %v", err)
	}
	if reads != 1 {
		t.Fatalf("rejected read reached volume: %d", reads)
	}
}
