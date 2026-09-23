package khora

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/sandbox"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestNetworkPatchUsesObservedRevisionAndCompleteDeclaration(t *testing.T) {
	ctx := context.Background()
	revision := strings.Repeat("a", 64)
	deny := "deny"
	patches := 0
	binding := sdk.SandboxBinding{SandboxID: "box", MutationRevision: revision, State: "paused", PlacementEpoch: 1, PlacementState: "lost", TemplateID: "template", CPUMillis: 1000, MemoryByte: 1 << 30, DiskByte: 1 << 30, IdleTimeout: "0s", Retention: "0s", Volumes: []sdk.VolumeBinding{}, Network: sdk.Network{Egress: sdk.Egress{Default: &deny}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/info":
			json.NewEncoder(w).Encode(map[string]any{"store_id": "store", "version": EngineRevision, "manual_cold_recovery": true})
		case "/v1/sandboxes/box":
			if r.Method != "PATCH" || r.Header.Get("If-Match") != fmt.Sprintf("%q", binding.MutationRevision) {
				http.Error(w, "stale", 412)
				return
			}
			var body sdk.PatchRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if body.Network == nil {
				t.Error("patch omitted network")
				w.WriteHeader(400)
				return
			}
			patches++
			binding.Network = *body.Network
			binding.MutationRevision = strings.Repeat("b", 64)
			// The engine applied the mutation but the reply is unusable.
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"reply lost"}`)
		case "/v1/sandboxes/box/binding":
			json.NewEncoder(w).Encode(binding)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	backend, err := New(ctx, Config{Endpoint: server.URL, StoreID: "store"})
	if err != nil {
		t.Fatal(err)
	}
	ref := sandbox.Reference{StoreID: "store", SandboxID: "box", MutationRevision: revision, Epoch: 1}
	target := sandbox.Network{Allow: []sandbox.Tuple{{CIDR: netip.MustParsePrefix("192.0.2.0/24"), Port: 443, Protocol: "tcp"}}, Via: []sandbox.Route{{Match: sandbox.Tuple{CIDR: netip.MustParsePrefix("10.80.0.1/32"), Port: 80, Protocol: "tcp"}, Endpoint: netip.MustParseAddrPort("192.0.2.1:8080")}}}
	if _, err = backend.UpdateNetwork(ctx, ref, target); err == nil {
		t.Fatal("uncertain reply reported success")
	}
	observed, err := backend.InspectConfiguration(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.SameConfiguration(observed, target, nil) || observed.TemplateID != "template" || observed.MemoryBytes != 1<<30 || observed.MutationRevision == revision {
		t.Fatal(observed)
	}
	if _, err = backend.UpdateNetwork(ctx, ref, target); !sandbox.Is(err, sandbox.Precondition) {
		t.Fatal("stale revision was reused as new authority", err)
	}
	if patches != 1 {
		t.Fatal("mutation duplicated", patches)
	}
}
