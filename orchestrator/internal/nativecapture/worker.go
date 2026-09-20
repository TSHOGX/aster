// Package nativecapture owns asynchronous capture of committed native snapshots.
package nativecapture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var ErrDeferred = errors.New("native source requires maintenance activation")

type Request struct {
	SessionID, UserID, Status, StateDigest, GenerationID, StoreID, SandboxID, NativeID, SnapshotPath, NativeDigest string
	StateVersion                                                                                                   int
	TurnID, Size                                                                                                   int64
	Epoch                                                                                                          uint64
	CreatedAt                                                                                                      time.Time
}
type Result struct {
	ArchivePath string
}
type Repository interface {
	PendingNativeCaptures(context.Context, int) ([]Request, error)
	CompleteNativeCapture(context.Context, Request, Result) error
	RequestNativeCaptureActivation(context.Context, Request) error
}
type Source interface {
	OpenNativeSnapshot(context.Context, Request) (io.ReadCloser, error)
}
type Sink interface {
	CaptureNativeSnapshot(context.Context, Request, io.Reader) (Result, error)
}
type Worker struct {
	Repository Repository
	Source     Source
	Sink       Sink
	mu         sync.Mutex
}

// Step processes one oldest pending version per session. The independent worker
// process must also hold the canonical control-database capture lock; the mutex
// prevents accidental overlap inside that process. Other sessions can progress
// when one source is paused or fails integrity verification.
func (w *Worker) Step(ctx context.Context, limit int) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Repository == nil || w.Source == nil || w.Sink == nil {
		return 0, fmt.Errorf("native capture repository, source and sink required")
	}
	requests, err := w.Repository.PendingNativeCaptures(ctx, limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	var failures []error
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(append(failures, err)...)
		}
		source, err := w.Source.OpenNativeSnapshot(ctx, request)
		if errors.Is(err, ErrDeferred) {
			if err := w.Repository.RequestNativeCaptureActivation(ctx, request); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("capture %s/%d: %w", request.SessionID, request.StateVersion, err))
			continue
		}
		result, err := w.Sink.CaptureNativeSnapshot(ctx, request, source)
		err = errors.Join(err, source.Close())
		if err == nil {
			err = w.Repository.CompleteNativeCapture(ctx, request, result)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("capture %s/%d: %w", request.SessionID, request.StateVersion, err))
			continue
		}
		completed++
	}
	return completed, errors.Join(failures...)
}
