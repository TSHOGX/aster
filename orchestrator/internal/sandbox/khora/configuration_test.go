package khora

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestConfigureReconcilesOnlyExactCompletePausedBinding(t *testing.T) {
	for _, fault := range []string{"none", "lost-reply", "other-mount", "other-network", "new-epoch", "running", "stopping", "retention", "store", "mismatch-success", "empty-via", "missing-volumes"} {
		t.Run(fault, func(t *testing.T) {
			calls := 0
			network := sandbox.Network{Allow: []sandbox.Tuple{{CIDR: netip.MustParsePrefix("192.0.2.0/24"), Port: 443, Protocol: "tcp"}}}
			mounts := []sandbox.Mount{{VolumeID: "published", Destination: "/root/.agents", Access: "shared-ro"}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/info" {
					identity := "store"
					if fault == "store" && calls > 0 {
						identity = "rebuilt"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"store_id": identity, "version": EngineRevision, "manual_cold_recovery": true})
					return
				}
				if r.Method == http.MethodPatch {
					calls++
					if r.Header.Get("If-Match") != "\""+strings.Repeat("a", 64)+"\"" {
						t.Error("configuration refreshed original validator")
					}
					if fault != "none" && fault != "mismatch-success" {
						http.Error(w, "stale", 412)
						return
					}
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if r.URL.Path != "/v1/sandboxes/sandbox/binding" {
					http.NotFound(w, r)
					return
				}
				state, placement, retention, epoch := "paused", "lost", "0s", 1
				wireMounts := []map[string]string{{"volume_id": "published", "path": "/root/.agents", "access": "shared-ro"}}
				var via []map[string]any
				allow := []map[string]any{{"cidr": "192.0.2.0/24", "port": 443, "proto": "tcp"}}
				switch fault {
				case "other-mount", "mismatch-success":
					wireMounts[0]["volume_id"] = "other"
				case "empty-via":
					via = []map[string]any{{"endpoint": "192.0.2.1:19030", "match": []any{}}}
				case "missing-volumes":
					wireMounts = nil
				case "other-network":
					allow[0]["port"] = 8443
				case "new-epoch":
					epoch = 2
				case "running":
					state, placement = "running", "active"
				case "stopping":
					placement = "stopping"
				case "retention":
					retention = "24h"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sandbox_id": "sandbox", "mutation_revision": strings.Repeat("b", 64), "state": state, "placement_epoch": epoch, "placement_state": placement, "idle_timeout": "0s", "retention": retention, "volumes": wireMounts, "network": map[string]any{"egress": map[string]any{"default": "deny", "allow": allow, "via": via}, "ingress": map[string]any{"publish": []any{}}}})
			}))
			defer server.Close()
			backend, err := New(context.Background(), Config{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			result, err := backend.Configure(context.Background(), sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 1, MutationRevision: strings.Repeat("a", 64)}, network, mounts)
			if fault == "none" || fault == "lost-reply" {
				if err != nil || result.MutationRevision != strings.Repeat("b", 64) {
					t.Fatalf("exact postcondition not accepted: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("%s accepted as intended configuration", fault)
			}
			if calls != 1 {
				t.Fatalf("configuration resent with refreshed authority: %d", calls)
			}
		})
	}
}

func TestConfigurationComparisonRequiresCompleteDeclaration(t *testing.T) {
	a := sandbox.Configuration{Network: sandbox.Network{Allow: []sandbox.Tuple{{CIDR: netip.MustParsePrefix("192.0.2.0/24"), Port: 443, Protocol: "tcp"}}}, Mounts: []sandbox.Mount{{VolumeID: "one", Destination: "/harness-config", Access: "shared-ro"}, {VolumeID: "two", Destination: "/root/.agents", Access: "shared-ro"}}}
	if !sandbox.SameConfiguration(a, a.Network, []sandbox.Mount{a.Mounts[1], a.Mounts[0]}) {
		t.Fatal("mount order changed semantics")
	}
	if sandbox.SameConfiguration(a, a.Network, a.Mounts[:1]) {
		t.Fatal("missing mount ignored")
	}
	if sandbox.SameConfiguration(a, sandbox.Network{}, a.Mounts) {
		t.Fatal("extra allowed route ignored")
	}
	if _, err := sandbox.CanonicalTuple(strings.Repeat("x", 4), 443, "tcp"); err == nil {
		t.Fatal("malformed route parsed")
	}
}
