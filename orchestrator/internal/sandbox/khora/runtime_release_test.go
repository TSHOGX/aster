package khora

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/agentwire"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
)

type successfulRuntimeVerifier struct {
	transport.UnimplementedProcessServiceHandler
	starts atomic.Int32
}

func (f *successfulRuntimeVerifier) Start(_ context.Context, r *connect.Request[v1.StartRequest], out *connect.ServerStream[v1.ProcessEvent]) error {
	f.starts.Add(1)
	return out.Send(&v1.ProcessEvent{ProcessHandle: r.Msg.ProcessHandle, PlacementEpoch: r.Msg.PlacementEpoch, Kind: &v1.ProcessEvent_End{End: &v1.EndEvent{ExitCode: 0}}})
}

func TestHostRejectsUnsupportedRuntimeBeforeItsVerifier(t *testing.T) {
	for _, version := range []int{2, runtimerelease.ManifestVersion} {
		digest := "sha256:" + strings.Repeat("a", 64)
		manifest := runtimerelease.Manifest{Version: version, HistoryProtocol: runtimerelease.HistoryProtocol, Mounts: runtimerelease.DefaultMountContract(), ScheduleProtocol: runtimerelease.ScheduleProtocol, NativeProtocol: runtimerelease.NativeProtocol, Platform: "linux/arm64", EngineModule: runtimerelease.EngineModule, EngineRevision: runtimerelease.EngineRevision, AgentProtocol: agentwire.Version, ReaderProtocol: runtimerelease.ReaderProtocol, Workspace: "/workspace", RuntimeState: "/runtime", PiPackage: runtimerelease.PiPackage, PiVersion: runtimerelease.PiVersion, PiIntegrity: runtimerelease.PiIntegrity, NodeVersion: runtimerelease.NodeVersion, NativeTree: digest, Files: map[string]string{}}
		if version == 2 {
			manifest.NativeProtocol = 0
		}
		for _, name := range runtimerelease.Components {
			manifest.Files[name] = digest
		}
		raw, err := runtimerelease.Bytes(manifest)
		if err != nil {
			t.Fatal(err)
		}
		digest, err = runtimerelease.Digest(raw)
		if err != nil {
			t.Fatal(err)
		}
		verifier := &successfulRuntimeVerifier{}
		mux := http.NewServeMux()
		route, handler := transport.NewProcessServiceHandler(verifier)
		mux.Handle(route, handler)
		mux.HandleFunc("/v1/info", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"store_id": "store", "version": EngineRevision, "manual_cold_recovery": true})
		})
		mux.HandleFunc("/v1/sandboxes/sandbox", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"sandbox_id": "sandbox", "state": "running", "placement_epoch": 1, "placement_state": "active", "mutation_revision": strings.Repeat("a", 64)})
		})
		mux.HandleFunc("/v1/sandboxes/sandbox/files", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("path") != runtimerelease.ManifestPath {
				t.Error("verification read escaped fixed manifest path")
			}
			w.Write(raw)
		})
		server := httptest.NewServer(mux)
		backend, err := New(context.Background(), Config{Endpoint: server.URL, StoreID: "store"})
		if err != nil {
			t.Fatal(err)
		}
		err = backend.VerifyRuntime(context.Background(), sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 1}, digest)
		server.Close()
		if version == 2 {
			if !sandbox.Is(err, sandbox.Incompatible) || verifier.starts.Load() != 0 {
				t.Fatal("unsupported self-verifier bypassed host capability requirements", err, verifier.starts.Load())
			}
		} else if err != nil || verifier.starts.Load() != 1 {
			t.Fatal("supported runtime did not reach self-verification", err, verifier.starts.Load())
		}
	}
}
