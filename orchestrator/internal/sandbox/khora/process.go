package khora

import (
	"context"
	"fmt"
	"io"
	"time"

	"harness-platform/orchestrator/internal/sandbox"

	"connectrpc.com/connect"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
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
	return &processStream{receiveStream: receiveStream[v1.ProcessEvent]{stream: stream, cancel: cancel}, reference: request.ProcessReference}, nil
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
	return &processStream{receiveStream: receiveStream[v1.ProcessEvent]{stream: stream, cancel: cancel}, reference: reference}, nil
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
	receiveStream[v1.ProcessEvent]
	reference sandbox.ProcessReference
}

func (s *processStream) Next() (sandbox.ProcessFrame, error) {
	event, err := s.receive()
	if err != nil {
		if err == io.EOF {
			return sandbox.ProcessFrame{}, io.EOF
		}
		return sandbox.ProcessFrame{}, processError("receive process output", err)
	}
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

func (b *Backend) CloseProcessInput(ctx context.Context, reference sandbox.ProcessReference) error {
	if err := b.validateProcess(ctx, reference); err != nil {
		return err
	}
	request := connect.NewRequest(&v1.CloseStdinRequest{ProcessHandle: reference.Handle, PlacementEpoch: reference.Epoch})
	request.Header().Set(sandboxHeader, reference.SandboxID)
	_, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).CloseStdin(ctx, request)
	return processError("close process input", err)
}

// StartPTY launches a process attached to a PTY of the given size. PTY is a
// field on the engine's Start request rather than a separate service, so this
// shares every identity and idempotency guarantee with StartProcess: the handle
// and epoch are supplied by the application before the first output frame.
func (b *Backend) StartPTY(ctx context.Context, request sandbox.StartProcessRequest, rows, cols int) (sandbox.ProcessStream, error) {
	if err := b.validateProcess(ctx, request.ProcessReference); err != nil {
		return nil, err
	}
	if request.IdempotencyKey == "" || request.Command == "" {
		return nil, failure("start pty", sandbox.Invalid, fmt.Errorf("persisted process key and command required"))
	}
	if rows < 1 || rows > 500 || cols < 1 || cols > 1000 {
		return nil, failure("start pty", sandbox.Invalid, fmt.Errorf("PTY dimensions out of range"))
	}
	streamCtx, cancel := context.WithCancel(ctx)
	message := connect.NewRequest(&v1.StartRequest{Cmd: request.Command, Args: request.Arguments, Env: request.Environment, Cwd: request.Directory, IdempotencyKey: request.IdempotencyKey, PlacementEpoch: request.Epoch, ProcessHandle: request.Handle, Pty: &v1.PtySize{Rows: uint32(rows), Cols: uint32(cols)}})
	message.Header().Set(sandboxHeader, request.SandboxID)
	stream, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).Start(streamCtx, message)
	if err != nil {
		cancel()
		return nil, processError("start pty", err)
	}
	return &processStream{receiveStream: receiveStream[v1.ProcessEvent]{stream: stream, cancel: cancel}, reference: request.ProcessReference}, nil
}

// ResizePTY converges a live PTY to new dimensions. It is idempotent and carries
// the placement epoch, so a resize can never land on a successor placement's
// process that happens to reuse a handle.
func (b *Backend) ResizePTY(ctx context.Context, reference sandbox.ProcessReference, rows, cols int) error {
	if err := b.validateProcess(ctx, reference); err != nil {
		return err
	}
	if rows < 1 || rows > 500 || cols < 1 || cols > 1000 {
		return failure("resize pty", sandbox.Invalid, fmt.Errorf("PTY dimensions out of range"))
	}
	request := connect.NewRequest(&v1.UpdateRequest{ProcessHandle: reference.Handle, PlacementEpoch: reference.Epoch, Pty: &v1.PtySize{Rows: uint32(rows), Cols: uint32(cols)}})
	request.Header().Set(sandboxHeader, reference.SandboxID)
	_, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).Update(ctx, request)
	return processError("resize pty", err)
}

// SendPTYInput writes bytes to a PTY master. It sets pty_in explicitly rather
// than relying on the guest's stdin handle happening to be the same descriptor
// for a PTY process: these are different input branches in the engine, and a
// terminal's keystrokes belong on the PTY one.
//
// A failed send may have written a prefix. The caller must not blindly retry: a
// repeated keystroke in a live shell is a new side effect, not a recovery.
func (b *Backend) SendPTYInput(ctx context.Context, reference sandbox.ProcessReference, data []byte) error {
	if err := b.validateProcess(ctx, reference); err != nil {
		return err
	}
	request := connect.NewRequest(&v1.SendInputRequest{ProcessHandle: reference.Handle, PlacementEpoch: reference.Epoch, Data: data, PtyIn: true})
	request.Header().Set(sandboxHeader, reference.SandboxID)
	_, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).SendInput(ctx, request)
	return processError("send pty input", err)
}

// InspectProcess uses a live guest scan for liveness. Durable control-plane
// records supply only retained exit evidence when a process is no longer live.
func (b *Backend) InspectProcess(ctx context.Context, ref sandbox.ProcessReference) (sandbox.ProcessObservation, error) {
	if err := b.validateProcess(ctx, ref); err != nil {
		return sandbox.ProcessObservation{}, err
	}
	request := connect.NewRequest(&v1.ListRequest{})
	request.Header().Set(sandboxHeader, ref.SandboxID)
	response, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).List(ctx, request)
	if err != nil {
		return sandbox.ProcessObservation{}, processError("inspect process", err)
	}
	for _, process := range response.Msg.Processes {
		if process.ProcessHandle != ref.Handle {
			continue
		}
		if process.PlacementEpoch != ref.Epoch {
			return sandbox.ProcessObservation{}, failure("inspect process", sandbox.Precondition, fmt.Errorf("process placement changed"))
		}
		observation := sandbox.ProcessObservation{ProcessReference: ref, State: process.State, PID: int(process.Pid)}
		if process.ExitCode != nil {
			code := int(*process.ExitCode)
			observation.ExitCode = &code
		}
		return observation, nil
	}
	snapshot, err := b.client.ProcessSnapshot(ctx, ref.Handle)
	if err != nil {
		return sandbox.ProcessObservation{}, mapError("inspect process", err)
	}
	if snapshot.ProcessID != ref.Handle || snapshot.SandboxID != ref.SandboxID || snapshot.PlacementEpoch != ref.Epoch {
		return sandbox.ProcessObservation{}, failure("inspect process", sandbox.Precondition, fmt.Errorf("retained process identity changed"))
	}
	if snapshot.State != "exited" && snapshot.State != "lost" {
		// A retained running row is not liveness. Only classify it as lost when
		// both its handle and its known PID are absent from the guest scan.
		// No exit code or descendant-cleanup outcome follows from this evidence.
		if snapshot.PID > 0 {
			// Start may have completed between the first scan and the retained
			// snapshot. Establish absence after observing that PID, not before.
			confirmed, err := transport.NewProcessServiceClient(b.httpClient, b.endpoint).List(ctx, request)
			if err != nil {
				return sandbox.ProcessObservation{}, processError("inspect process", err)
			}
			for _, process := range confirmed.Msg.Processes {
				if process.ProcessHandle == ref.Handle {
					if process.PlacementEpoch != ref.Epoch {
						return sandbox.ProcessObservation{}, failure("inspect process", sandbox.Precondition, fmt.Errorf("process placement changed"))
					}
					observation := sandbox.ProcessObservation{ProcessReference: ref, State: process.State, PID: int(process.Pid)}
					if process.ExitCode != nil {
						code := int(*process.ExitCode)
						observation.ExitCode = &code
					}
					return observation, nil
				}
			}
			present := false
			for _, pid := range confirmed.Msg.Pids {
				present = present || int(pid) == snapshot.PID
			}
			if !present {
				// The guest may have retired its live registry entry while the
				// transport still retains the terminal frame. Prefer that exact
				// evidence over an unknown exit/cleanup result.
				if code, ok := b.retainedProcessExit(ctx, ref); ok {
					return sandbox.ProcessObservation{ProcessReference: ref, State: "exited", ExitCode: &code}, nil
				}
				return sandbox.ProcessObservation{ProcessReference: ref, State: "lost"}, nil
			}
		}
		return sandbox.ProcessObservation{ProcessReference: ref, State: "unknown"}, nil
	}
	return sandbox.ProcessObservation{ProcessReference: ref, State: snapshot.State, PID: snapshot.PID, ExitCode: snapshot.ExitCode}, nil
}

func (b *Backend) retainedProcessExit(ctx context.Context, ref sandbox.ProcessReference) (int, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stream, err := b.ConnectProcess(ctx, ref, 0)
	if err != nil {
		return 0, false
	}
	defer stream.Close()
	for n := 0; n < 4096; n++ {
		frame, err := stream.Next()
		if err != nil {
			return 0, false
		}
		if frame.Kind == "end" {
			return int(frame.ExitCode), true
		}
	}
	return 0, false
}
