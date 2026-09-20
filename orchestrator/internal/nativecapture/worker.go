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
	PendingNativeCaptures(context.Context, string, int) ([]Request, error)
	CompleteNativeCapture(context.Context, Request, Result) error
	RecordNativeCaptureFailure(context.Context, Request, string, time.Time) error
	RequestNativeCaptureActivation(context.Context, Request) error
}
type Source interface {
	OpenNativeSnapshot(context.Context, Request) (io.ReadCloser, error)
}
type Sink interface {
	CaptureNativeSnapshot(context.Context, Request, io.Reader) (Result, error)
}
type Worker struct {
	Repository  Repository
	Source      Source
	Sink        Sink
	mu          sync.Mutex
	after       string
	ItemTimeout time.Duration
}

// Step processes one oldest pending version per session with a rotating session
// cursor and per-item deadline. Failed pages cannot hide later sessions. The independent worker
// process must also hold the canonical control-database capture lock; the mutex
// prevents accidental overlap inside that process. Other sessions can progress
// when one source is paused or fails integrity verification.
func (w *Worker) Step(ctx context.Context, limit int) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Repository == nil || w.Source == nil || w.Sink == nil {
		return 0, fmt.Errorf("native capture repository, source and sink required")
	}

	requests, err := w.Repository.PendingNativeCaptures(ctx, w.after, limit)
	if err != nil {
		return 0, err
	}
	if len(requests) == 0 && w.after != "" {
		w.after = ""
		requests, err = w.Repository.PendingNativeCaptures(ctx, "", limit)
		if err != nil {
			return 0, err
		}
	}
	completed := 0
	var failures []error
	timeout := w.ItemTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return completed, errors.Join(append(failures, err)...)
		}
		w.after = request.SessionID
		item, cancel := context.WithTimeout(ctx, timeout)
		captured, err := w.capture(item, request)
		cancel()
		if captured {
			completed++
		}
		if err != nil {
			if ctx.Err() == nil {
				settle, stop := context.WithTimeout(ctx, time.Second)
				err = errors.Join(err, w.Repository.RecordNativeCaptureFailure(settle, request, err.Error(), time.Now().UTC()))
				stop()
			}
			failures = append(failures, fmt.Errorf("capture %s/%d: %w", request.SessionID, request.StateVersion, err))
		}
	}
	return completed, errors.Join(failures...)
}
func (w *Worker) capture(ctx context.Context, request Request) (bool, error) {
	source, err := w.Source.OpenNativeSnapshot(ctx, request)
	if errors.Is(err, ErrDeferred) {
		return false, w.Repository.RequestNativeCaptureActivation(ctx, request)
	}
	if err != nil {
		return false, err
	}
	result, err := w.Sink.CaptureNativeSnapshot(ctx, request, source)
	err = errors.Join(err, source.Close())
	if err == nil {
		err = w.Repository.CompleteNativeCapture(ctx, request, result)
	}
	return err == nil, err
}
