package khora

import (
	"connectrpc.com/connect"
	"context"
	"fmt"
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	transport "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1/transportv1connect"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
	"path"
	"strings"
)

func (b *Backend) WatchWorkspace(ctx context.Context, ref sandbox.Reference, workspace string, after uint64) (sandbox.FileWatch, error) {
	if !path.IsAbs(workspace) || path.Clean(workspace) != workspace || workspace == "/" {
		return nil, failure("watch workspace", sandbox.Invalid, fmt.Errorf("invalid workspace root"))
	}
	if err := b.validateReference(ctx, ref, false); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	request := connect.NewRequest(&v1.WatchRequest{Path: workspace, Recursive: true, PlacementEpoch: ref.Epoch, AfterSequence: after})
	request.Header().Set(sandboxHeader, ref.SandboxID)
	// Connect can wait for the first response frame. A resumed, quiet watch has
	// none yet; acquiring the subscription must not block the HTTP headers or
	// the caller that will produce the next change. Next owns that wait.
	w := &pendingFileWatch{ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	go func() {
		stream, err := transport.NewFsServiceClient(b.httpClient, b.endpoint).Watch(ctx, request)
		if err != nil {
			w.err = processError("watch workspace", err)
		} else {
			w.watch = &fileWatch{receiveStream: receiveStream[v1.WatchEvent]{stream: stream, cancel: cancel}, epoch: ref.Epoch, workspace: workspace}
			if ctx.Err() != nil {
				_ = w.watch.Close()
			}
		}
		close(w.ready)
	}()
	return w, nil
}

type pendingFileWatch struct {
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{}
	watch  *fileWatch
	err    error
}

func (w *pendingFileWatch) Next() (sandbox.FileInvalidation, error) {
	select {
	case <-w.ctx.Done():
		return sandbox.FileInvalidation{}, w.ctx.Err()
	case <-w.ready:
	}
	if w.err != nil {
		return sandbox.FileInvalidation{}, w.err
	}
	return w.watch.Next()
}
func (w *pendingFileWatch) Close() error {
	w.cancel()
	select {
	case <-w.ready:
		if w.watch != nil {
			return w.watch.Close()
		}
	default:
	}
	return nil
}

type fileWatch struct {
	receiveStream[v1.WatchEvent]
	epoch     uint64
	workspace string
}

func (w *fileWatch) Next() (sandbox.FileInvalidation, error) {
	e, err := w.receive()
	if err != nil {
		if err == io.EOF {
			return sandbox.FileInvalidation{}, io.EOF
		}
		return sandbox.FileInvalidation{}, processError("watch workspace", err)
	}
	return workspaceInvalidation(e, w.epoch, w.workspace), nil
}

func workspaceInvalidation(e *v1.WatchEvent, epoch uint64, workspace string) sandbox.FileInvalidation {
	out := sandbox.FileInvalidation{Sequence: e.Sequence, Epoch: e.PlacementEpoch, Reset: e.Overflow || e.Reset_ || e.PlacementEpoch != epoch, Operation: e.Operation, Reason: e.ResetReason}
	normalize := func(name string) (string, bool) {
		if name == "" {
			return "", true
		}
		clean := path.Clean(name)
		if clean == workspace {
			return "", true
		}
		if !strings.HasPrefix(clean, workspace+"/") {
			return "", false
		}
		return strings.TrimPrefix(clean, workspace+"/"), true
	}
	var ok, oldOK bool
	out.Path, ok = normalize(e.Path)
	out.OldPath, oldOK = normalize(e.OldPath)
	if !ok || !oldOK {
		out.Reset = true
		out.Reason = "invalid_path"
		out.Path = ""
		out.OldPath = ""
	}
	if e.PlacementEpoch != epoch {
		out.Reason = "placement"
	}
	if e.Overflow && out.Reason == "" {
		out.Reason = "overflow"
	}
	switch e.Type {
	case v1.EntryType_ENTRY_TYPE_DIRECTORY:
		out.EntryType = "directory"
	case v1.EntryType_ENTRY_TYPE_FILE:
		out.EntryType = "file"
	default:
		out.EntryType = "unknown"
	}
	return out
}
