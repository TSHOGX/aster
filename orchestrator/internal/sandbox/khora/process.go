package khora

import (
	"context"
	"fmt"
	"io"

	"connectrpc.com/connect"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
	"harness-platform/orchestrator/internal/sandbox"
)

const sandboxHeader = "Engine-Sandbox-Id"

func (b *Backend) StartProcess(ctx context.Context, request sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	if err := b.validateProcess(ctx, request.ProcessReference); err != nil {
		return nil, err
	}
	if request.IdempotencyKey == "" || request.Command == "" {
		return nil, failure("start process", sandbox.Invalid, fmt.Errorf("persisted process key and command required"))
	}
	streamCtx, cancel := context.WithCancel(ctx)
	message := connect.NewRequest(&v1.StartRequest{Cmd: request.Command, Args: request.Arguments, Env: request.Environment, Cwd: request.Directory, IdempotencyKey: request.IdempotencyKey, PlacementEpoch: request.Epoch, ProcessHandle: request.Handle})
	message.Header().Set(sandboxHeader, request.SandboxID)
	stream, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).Start(streamCtx, message)
	if err != nil {
		cancel()
		return nil, processError("start process", err)
	}
	return &processStream{stream: stream, reference: request.ProcessReference, cancel: cancel}, nil
}

func (b *Backend) ConnectProcess(ctx context.Context, reference sandbox.ProcessReference, after uint64) (sandbox.ProcessStream, error) {
	if err := b.validateProcess(ctx, reference); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	request := connect.NewRequest(&v1.ConnectRequest{ProcessHandle: reference.Handle, PlacementEpoch: reference.Epoch, AfterSequence: after})
	request.Header().Set(sandboxHeader, reference.SandboxID)
	stream, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).Connect(streamCtx, request)
	if err != nil {
		cancel()
		return nil, processError("connect process", err)
	}
	return &processStream{stream: stream, reference: reference, cancel: cancel}, nil
}

func (b *Backend) SendProcessInput(ctx context.Context, reference sandbox.ProcessReference, data []byte) error {
	if err := b.validateProcess(ctx, reference); err != nil {
		return err
	}
	request := connect.NewRequest(&v1.SendInputRequest{ProcessHandle: reference.Handle, PlacementEpoch: reference.Epoch, Data: data})
	request.Header().Set(sandboxHeader, reference.SandboxID)
	_, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).SendInput(ctx, request)
	return processError("send process input", err)
}

func (b *Backend) SignalProcess(ctx context.Context, reference sandbox.ProcessReference, signal int32) error {
	if err := b.validateProcess(ctx, reference); err != nil {
		return err
	}
	if signal != 2 && signal != 15 && signal != 9 {
		return failure("signal process", sandbox.Invalid, fmt.Errorf("unsupported process cancellation signal"))
	}
	request := connect.NewRequest(&v1.SendSignalRequest{ProcessHandle: reference.Handle, PlacementEpoch: reference.Epoch, Signal: signal})
	request.Header().Set(sandboxHeader, reference.SandboxID)
	_, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).SendSignal(ctx, request)
	return processError("signal process", err)
}

func (b *Backend) validateProcess(ctx context.Context, reference sandbox.ProcessReference) error {
	if reference.Handle == "" || reference.Epoch == 0 {
		return failure("process reference", sandbox.Invalid, fmt.Errorf("durable process handle and placement epoch required"))
	}
	return b.validateReference(ctx, sandbox.Reference{StoreID: reference.StoreID, SandboxID: reference.SandboxID}, false)
}

type processStream struct {
	stream    *connect.ServerStreamForClient[v1.ProcessEvent]
	reference sandbox.ProcessReference
	cancel    context.CancelFunc
}

func (s *processStream) Next() (sandbox.ProcessFrame, error) {
	if !s.stream.Receive() {
		if err := s.stream.Err(); err != nil {
			return sandbox.ProcessFrame{}, processError("receive process output", err)
		}
		return sandbox.ProcessFrame{}, io.EOF
	}
	event := s.stream.Msg()
	if event.ProcessHandle != s.reference.Handle || event.PlacementEpoch != s.reference.Epoch {
		_ = s.Close()
		return sandbox.ProcessFrame{}, failure("receive process output", sandbox.Precondition, fmt.Errorf("process frame belongs to another handle or placement"))
	}
	frame := sandbox.ProcessFrame{Sequence: event.Sequence, Replay: event.Replay, Gap: event.Gap, ResetReason: event.ResetReason}
	switch payload := event.Kind.(type) {
	case *v1.ProcessEvent_Start:
		if payload.Start.ProcessHandle != s.reference.Handle || payload.Start.PlacementEpoch != s.reference.Epoch {
			_ = s.Close()
			return sandbox.ProcessFrame{}, failure("receive process start", sandbox.Precondition, fmt.Errorf("process start identity mismatch"))
		}
		frame.Kind = "start"
	case *v1.ProcessEvent_Data:
		frame.Kind = "data"
		frame.Stream = payload.Data.Stream
		frame.Data = append([]byte(nil), payload.Data.Data...)
	case *v1.ProcessEvent_End:
		frame.Kind = "end"
		frame.ExitCode = payload.End.ExitCode
	case *v1.ProcessEvent_Keepalive:
		frame.Kind = "keepalive"
	default:
		_ = s.Close()
		return sandbox.ProcessFrame{}, failure("receive process output", sandbox.Incompatible, fmt.Errorf("unknown process frame"))
	}
	return frame, nil
}
func (s *processStream) Close() error { s.cancel(); return s.stream.Close() }

func processError(operation string, err error) error {
	if err == nil {
		return nil
	}
	class := sandbox.Unavailable
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		class = sandbox.Invalid
	case connect.CodeNotFound:
		class = sandbox.NotFound
	case connect.CodeAlreadyExists:
		class = sandbox.Conflict
	case connect.CodeFailedPrecondition, connect.CodeAborted:
		class = sandbox.Precondition
	case connect.CodeResourceExhausted:
		class = sandbox.Capacity
		if operation == "receive process output" {
			class = sandbox.Backpressure
		}
	case connect.CodeUnimplemented:
		class = sandbox.Incompatible
	}
	return failure(operation, class, err)
}

var _ sandbox.Processes = (*Backend)(nil)
