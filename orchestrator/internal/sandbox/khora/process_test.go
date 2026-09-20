package khora

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"

	"connectrpc.com/connect"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
)

type processFixture struct {
	transport.UnimplementedProcessServiceHandler
	mu      sync.Mutex
	start   *v1.StartRequest
	connect *v1.ConnectRequest
	header  string
	signals []int32
	inputs  [][]byte
	frames  []*v1.ProcessEvent
}

func (f *processFixture) Start(_ context.Context, request *connect.Request[v1.StartRequest], stream *connect.ServerStream[v1.ProcessEvent]) error {
	f.mu.Lock()
	f.start = request.Msg
	f.header = request.Header().Get(sandboxHeader)
	frames := f.frames
	f.mu.Unlock()
	for _, frame := range frames {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	return nil
}
func (f *processFixture) Connect(_ context.Context, request *connect.Request[v1.ConnectRequest], stream *connect.ServerStream[v1.ProcessEvent]) error {
	f.mu.Lock()
	f.connect = request.Msg
	f.header = request.Header().Get(sandboxHeader)
	frames := f.frames
	f.mu.Unlock()
	for _, frame := range frames {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	return nil
}
func (f *processFixture) SendInput(_ context.Context, request *connect.Request[v1.SendInputRequest]) (*connect.Response[v1.SendInputResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Msg.ProcessHandle != "process" || request.Msg.PlacementEpoch != 7 || request.Header().Get(sandboxHeader) != "sandbox" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, io.ErrUnexpectedEOF)
	}
	f.inputs = append(f.inputs, append([]byte(nil), request.Msg.Data...))
	return connect.NewResponse(&v1.SendInputResponse{}), nil
}
func (f *processFixture) SendSignal(_ context.Context, request *connect.Request[v1.SendSignalRequest]) (*connect.Response[v1.SendSignalResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Msg.ProcessHandle != "process" || request.Msg.PlacementEpoch != 7 || request.Header().Get(sandboxHeader) != "sandbox" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, io.ErrUnexpectedEOF)
	}
	f.signals = append(f.signals, request.Msg.Signal)
	return connect.NewResponse(&v1.SendSignalResponse{}), nil
}
func processBackend(t *testing.T, frames ...*v1.ProcessEvent) (*Backend, *processFixture, sandbox.ProcessReference) {
	t.Helper()
	fixture := &processFixture{frames: frames}
	mux := http.NewServeMux()
	route, handler := transport.NewProcessServiceHandler(fixture)
	mux.Handle(route, handler)
	mux.HandleFunc("/v1/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"store_id": "store", "version": EngineRevision, "manual_cold_recovery": true})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	backend, err := New(context.Background(), Config{Endpoint: server.URL, StoreID: "store"})
	if err != nil {
		t.Fatal(err)
	}
	return backend, fixture, sandbox.ProcessReference{StoreID: "store", SandboxID: "sandbox", Handle: "process", Epoch: 7}
}
func dataFrame(handle string, epoch uint64) *v1.ProcessEvent {
	return &v1.ProcessEvent{ProcessHandle: handle, PlacementEpoch: epoch, Sequence: 12, Kind: &v1.ProcessEvent_Data{Data: &v1.DataEvent{Stream: "stdout", Data: []byte("frame")}}}
}

func TestStartProcessPreservesDurableIdentityAndDetachDoesNotSignal(t *testing.T) {
	backend, fixture, reference := processBackend(t, dataFrame("process", 7))
	stream, err := backend.StartProcess(context.Background(), sandbox.StartProcessRequest{ProcessReference: reference, IdempotencyKey: "start-key", Command: "/usr/local/bin/harness-bridge", Arguments: []string{"--stdio"}, Directory: "/workspace", Environment: []string{"HOME=/root"}})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := stream.Next()
	if err != nil || frame.Kind != "data" || string(frame.Data) != "frame" {
		t.Fatalf("output: %+v %v", frame, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.start == nil || fixture.start.IdempotencyKey != "start-key" || fixture.start.ProcessHandle != reference.Handle || fixture.start.PlacementEpoch != reference.Epoch || fixture.header != "sandbox" || fixture.start.Pty != nil {
		t.Fatalf("start lost trusted identity or requested a PTY: %+v", fixture.start)
	}
	if len(fixture.signals) != 0 {
		t.Fatal("detaching stream killed the process")
	}
}

func TestProcessReconnectPreservesCursorAndGapMarkers(t *testing.T) {
	event := dataFrame("process", 7)
	event.Replay = true
	event.Gap = true
	event.ResetReason = "replay_gap"
	backend, fixture, reference := processBackend(t, event)
	stream, err := backend.ConnectProcess(context.Background(), reference, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	frame, err := stream.Next()
	if err != nil || !frame.Gap || !frame.Replay || frame.ResetReason != "replay_gap" || frame.Sequence != 12 {
		t.Fatalf("lost recovery markers: %+v %v", frame, err)
	}
	if _, err := stream.Next(); err != io.EOF {
		t.Fatalf("stream end: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.connect.AfterSequence != 9 || fixture.connect.ProcessHandle != reference.Handle || fixture.connect.PlacementEpoch != reference.Epoch {
		t.Fatalf("reconnect changed identity/cursor: %+v", fixture.connect)
	}
}

func TestProcessFramesRejectAnotherHandleOrEpoch(t *testing.T) {
	for _, event := range []*v1.ProcessEvent{dataFrame("other", 7), dataFrame("process", 8), {ProcessHandle: "process", PlacementEpoch: 7, Kind: &v1.ProcessEvent_Start{Start: &v1.StartEvent{ProcessHandle: "other", PlacementEpoch: 7}}}} {
		backend, _, reference := processBackend(t, event)
		stream, err := backend.ConnectProcess(context.Background(), reference, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Next(); !sandbox.Is(err, sandbox.Precondition) {
			t.Fatalf("foreign process output accepted: %v", err)
		}
		stream.Close()
	}
}

func TestProcessInputAndSignalsAreExplicitlyScoped(t *testing.T) {
	backend, fixture, reference := processBackend(t)
	if err := backend.SendProcessInput(context.Background(), reference, []byte("command")); err != nil {
		t.Fatal(err)
	}
	if err := backend.SignalProcess(context.Background(), reference, 2); err != nil {
		t.Fatal(err)
	}
	if err := backend.SignalProcess(context.Background(), reference, -1); !sandbox.Is(err, sandbox.Invalid) {
		t.Fatalf("unscoped cancellation accepted: %v", err)
	}
	stale := reference
	stale.Epoch = 8
	if err := backend.SendProcessInput(context.Background(), stale, []byte("stale")); !sandbox.Is(err, sandbox.Precondition) {
		t.Fatalf("stale process input accepted: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.inputs) != 1 || len(fixture.signals) != 1 || fixture.signals[0] != 2 {
		t.Fatalf("unexpected control operations: %+v %+v", fixture.inputs, fixture.signals)
	}
}

func TestProcessOverflowIsBackpressureNotNodeCapacity(t *testing.T) {
	err := processError("receive process output", connect.NewError(connect.CodeResourceExhausted, io.ErrShortBuffer))
	if !sandbox.Is(err, sandbox.Backpressure) || sandbox.Is(err, sandbox.Capacity) {
		t.Fatalf("stream overflow misclassified as node quota: %v", err)
	}
}
