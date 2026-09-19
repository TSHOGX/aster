package nativecapture

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type workerRepository struct {
	requests  []Request
	completed []string
	fail      bool
}

func (r *workerRepository) PendingNativeCaptures(context.Context, int) ([]Request, error) {
	return r.requests, nil
}
func (r *workerRepository) CompleteNativeCapture(_ context.Context, p Request, _ Result) error {
	if r.fail {
		r.fail = false
		return errors.New("lost receipt commit")
	}
	r.completed = append(r.completed, p.SessionID)
	return nil
}

type workerSource struct{}

func (workerSource) OpenNativeSnapshot(_ context.Context, p Request) (io.ReadCloser, error) {
	if p.SessionID == "paused" {
		return nil, ErrDeferred
	}
	if p.SessionID == "missing" {
		return nil, errors.New("missing native bytes")
	}
	return io.NopCloser(strings.NewReader("native bytes")), nil
}

type workerSink struct{ calls int }

func (s *workerSink) CaptureNativeSnapshot(context.Context, Request, io.Reader) (Result, error) {
	s.calls++
	return Result{ArchivePath: "immutable"}, nil
}
func TestWorkerLeavesFailedCapturePendingAndProgressesOtherSessions(t *testing.T) {
	repository := &workerRepository{requests: []Request{{SessionID: "paused"}, {SessionID: "missing"}, {SessionID: "ready"}}}
	sink := &workerSink{}
	worker := Worker{Repository: repository, Source: workerSource{}, Sink: sink}
	n, err := worker.Step(context.Background(), 32)
	if n != 1 || err == nil || len(repository.completed) != 1 || repository.completed[0] != "ready" || sink.calls != 1 {
		t.Fatalf("capture batch: %d %v %+v", n, err, repository.completed)
	}
	repository.requests = []Request{{SessionID: "retry"}}
	repository.fail = true
	if n, err := worker.Step(context.Background(), 32); n != 0 || err == nil {
		t.Fatal("failed receipt was acknowledged")
	}
	if n, err := worker.Step(context.Background(), 32); n != 1 || err != nil {
		t.Fatalf("receipt retry failed: %d %v", n, err)
	}
}
