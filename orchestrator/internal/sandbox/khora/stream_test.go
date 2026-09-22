package khora

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
	"harness-platform/orchestrator/internal/sandbox"
)

type idleProcessStream struct {
	transport.UnimplementedProcessServiceHandler
}
type idleFileStream struct {
	transport.UnimplementedFsServiceHandler
}

func (*idleProcessStream) Connect(ctx context.Context, _ *connect.Request[v1.ConnectRequest], stream *connect.ServerStream[v1.ProcessEvent]) error {
	if err := stream.Send(dataFrame("process", 7)); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func (*idleFileStream) Watch(ctx context.Context, _ *connect.Request[v1.WatchRequest], stream *connect.ServerStream[v1.WatchEvent]) error {
	if err := stream.Send(&v1.WatchEvent{PlacementEpoch: 7}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestStreamConcurrentReceiveAndRepeatedClose(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(t *testing.T) {
			mux := http.NewServeMux()
			mux.Handle(transport.NewProcessServiceHandler(&idleProcessStream{}))
			mux.Handle(transport.NewFsServiceHandler(&idleFileStream{}))
			mux.HandleFunc("/v1/info", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"store_id": "store", "version": EngineRevision, "manual_cold_recovery": true})
			})
			server := httptest.NewUnstartedServer(mux)
			server.EnableHTTP2 = h2
			server.StartTLS()
			defer server.Close()
			backend, err := New(context.Background(), Config{HTTPClient: server.Client(), Endpoint: server.URL, StoreID: "store"})
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"process", "watch"} {
				t.Run(kind, func(t *testing.T) {
					for n := 0; n < 40; n++ {
						var next func() error
						var closeStream func() error
						ctx, cancel := context.WithCancel(context.Background())
						if kind == "process" {
							s, err := backend.ConnectProcess(ctx, sandbox.ProcessReference{StoreID: "store", SandboxID: "sandbox", Handle: "process", Epoch: 7}, 0)
							if err != nil {
								t.Fatal(err)
							}
							next = func() error { _, err := s.Next(); return err }
							closeStream = s.Close
						} else {
							s, err := backend.WatchWorkspace(ctx, sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 7}, 0)
							if err != nil {
								t.Fatal(err)
							}
							next = func() error { _, err := s.Next(); return err }
							closeStream = s.Close
						}
						if err := next(); err != nil {
							t.Fatal(err)
						}
						done := make(chan struct{})
						go func() {
							var wg sync.WaitGroup
							wg.Add(5)
							go func() { defer wg.Done(); _ = next() }()
							for i := 0; i < 4; i++ {
								go func() { defer wg.Done(); _ = closeStream() }()
							}
							wg.Wait()
							close(done)
						}()
						select {
						case <-done:
						case <-time.After(2 * time.Second):
							cancel()
							t.Fatal("detaching a live stream blocked")
						}
						cancel()
					}
				})
			}
		})
	}
}
