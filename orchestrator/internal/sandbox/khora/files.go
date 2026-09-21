package khora

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
)

func (b *Backend) WatchWorkspace(ctx context.Context, ref sandbox.Reference, after uint64) (sandbox.FileWatch, error) {
	if err := b.validateReference(ctx, ref, false); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	request := connect.NewRequest(&v1.WatchRequest{Path: "/workspace", Recursive: true, PlacementEpoch: ref.Epoch, AfterSequence: after})
	request.Header().Set(sandboxHeader, ref.SandboxID)
	stream, err := transport.NewFsServiceClient(b.httpClient, b.endpoint).Watch(ctx, request)
	if err != nil {
		cancel()
		return nil, processError("watch workspace", err)
	}
	return &fileWatch{stream: stream, cancel: cancel, epoch: ref.Epoch}, nil
}

type fileWatch struct {
	stream *connect.ServerStreamForClient[v1.WatchEvent]
	cancel context.CancelFunc
	epoch  uint64
}

func (w *fileWatch) Close() error { w.cancel(); return w.stream.Close() }
func (w *fileWatch) Next() (sandbox.FileInvalidation, error) {
	if !w.stream.Receive() {
		if err := w.stream.Err(); err != nil {
			return sandbox.FileInvalidation{}, processError("watch workspace", err)
		}
		return sandbox.FileInvalidation{}, io.EOF
	}
	e := w.stream.Msg()
	return sandbox.FileInvalidation{Sequence: e.Sequence, Epoch: e.PlacementEpoch, Reset: e.Overflow || e.Reset_ || e.PlacementEpoch != w.epoch}, nil
}
