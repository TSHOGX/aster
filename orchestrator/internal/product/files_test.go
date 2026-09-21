package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

type localFileBackend struct {
	root      string
	mu        sync.Mutex
	processes map[string]*localFileProcess
	starts    int
	loseEnd   bool
}
type localFileProcess struct {
	frames  chan sandbox.ProcessFrame
	reader  *io.PipeReader
	input   *io.PipeWriter
	closed  chan struct{}
	once    sync.Once
	loseEnd bool
}

func (p *localFileProcess) Next() (sandbox.ProcessFrame, error) {
	select {
	case f := <-p.frames:
		if f.Kind == "end" && p.loseEnd {
			return sandbox.ProcessFrame{}, io.ErrUnexpectedEOF
		}
		return f, nil
	case <-p.closed:
		return sandbox.ProcessFrame{}, io.EOF
	}
}
func (p *localFileProcess) Close() error { p.once.Do(func() { close(p.closed) }); return nil }

type localFileOutput struct{ p *localFileProcess }

func (w localFileOutput) Write(b []byte) (int, error) {
	select {
	case w.p.frames <- sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: append([]byte(nil), b...)}:
		return len(b), nil
	case <-w.p.closed:
		return 0, io.ErrClosedPipe
	}
}
func (b *localFileBackend) StartProcess(_ context.Context, r sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var request projectfiles.Request
	if err := json.Unmarshal([]byte(r.Arguments[0]), &request); err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	p := &localFileProcess{reader: reader, input: writer, frames: make(chan sandbox.ProcessFrame, 8), closed: make(chan struct{}), loseEnd: b.loseEnd}
	b.loseEnd = false
	b.starts++
	b.processes[r.Handle] = p
	p.frames <- sandbox.ProcessFrame{Kind: "start"}
	go func() {
		defer reader.Close()
		err := projectfiles.Run(b.root, request, reader, localFileOutput{p})
		code := int32(0)
		if err != nil {
			code = 1
		}
		select {
		case p.frames <- sandbox.ProcessFrame{Kind: "end", ExitCode: code}:
		case <-p.closed:
		}
	}()
	return p, nil
}
func (b *localFileBackend) SendProcessInput(_ context.Context, r sandbox.ProcessReference, data []byte) error {
	b.mu.Lock()
	p := b.processes[r.Handle]
	b.mu.Unlock()
	_, err := p.input.Write(data)
	return err
}
func (b *localFileBackend) CloseProcessInput(_ context.Context, r sandbox.ProcessReference) error {
	b.mu.Lock()
	p := b.processes[r.Handle]
	b.mu.Unlock()
	return p.input.Close()
}
func (b *localFileBackend) SignalProcess(_ context.Context, r sandbox.ProcessReference, _ int32) error {
	b.mu.Lock()
	p := b.processes[r.Handle]
	b.mu.Unlock()
	if p == nil {
		return &sandbox.Error{Class: sandbox.NotFound}
	}
	_ = p.reader.Close()
	return &sandbox.Error{Class: sandbox.NotFound}
}
func (b *localFileBackend) ConnectProcess(context.Context, sandbox.ProcessReference, uint64) (sandbox.ProcessStream, error) {
	return nil, &sandbox.Error{Class: sandbox.NotFound}
}
func (b *localFileBackend) WatchWorkspace(context.Context, sandbox.Reference, uint64) (sandbox.FileWatch, error) {
	return nil, errors.New("not configured")
}
func fileFixture(t *testing.T) (*Files, Scope, *localFileBackend) {
	t.Helper()
	s, a, _ := fixture(t)
	backend := fakeBackend()
	l := &Lifecycle{Store: s, Backend: backend, MaxProjects: 2}
	profile := testProfile()
	must(t, l.EnsureActive(ctx, a, profile))
	drive(t, l, a.ProjectID)
	files := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	return &Files{Store: s, Lifecycle: l, Backend: files, Profile: profile}, a, files
}
func writeRequest(path, content string) projectfiles.Request {
	return projectfiles.Request{Operation: "write", Path: path, Size: int64(len(content)), Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}
}
func TestFilesLostReplyNeverReplaysOverSibling(t *testing.T) {
	f, a, b := fileFixture(t)
	b.loseEnd = true
	request := writeRequest("same", "first")
	o, err := f.Run(ctx, a, "lost", request, strings.NewReader("first"), nil)
	wantError(t, err, ErrFileOutcomeUnknown)
	if o.State != "unknown" || o.Settled {
		t.Fatal(o)
	}
	must(t, os.WriteFile(filepath.Join(b.root, "same"), []byte("sibling"), 0600))
	// A different operation can proceed; there is no workspace lock.
	_, err = f.Run(ctx, a, "other", writeRequest("other", "ok"), strings.NewReader("ok"), nil)
	must(t, err)
	_, err = f.Run(ctx, a, "lost", request, strings.NewReader("first"), nil)
	wantError(t, err, ErrFileOutcomeUnknown)
	must(t, f.Reconcile(ctx, 32))
	got, err := os.ReadFile(filepath.Join(b.root, "same"))
	must(t, err)
	if string(got) != "sibling" || b.starts != 2 {
		t.Fatal("mutation replayed", string(got), b.starts)
	}
	o, err = f.Store.FileOperation(ctx, a, "lost")
	must(t, err)
	if o.State != "unknown" || !o.Settled {
		t.Fatal(o)
	}
	blockers, err := f.Store.Blockers(ctx, a)
	must(t, err)
	if len(blockers) != 0 {
		t.Fatal(blockers)
	}
}
func TestFilesAdmissionOwnershipAndConcurrentTurns(t *testing.T) {
	f, a, b := fileFixture(t)
	_, err := f.Store.CreateSession(ctx, a, "agent", "Agent")
	must(t, err)
	_, err = f.Store.AdmitTurn(ctx, a, "agent", "turn", "work", false)
	must(t, err)
	request := writeRequest("data", "content")
	_, err = f.Run(ctx, Scope{"bob", a.ProjectID}, "foreign", request, strings.NewReader("content"), nil)
	wantError(t, err, ErrNotFound)
	if b.starts != 0 {
		t.Fatal("unauthorized process")
	}
	_, err = f.Run(ctx, a, "valid", request, strings.NewReader("content"), nil)
	must(t, err)
	_, err = f.Run(ctx, a, "valid", writeRequest("data", "changed"), strings.NewReader("changed"), nil)
	wantError(t, err, ErrConflict)
	var out bytes.Buffer
	_, err = f.Run(ctx, a, "read", projectfiles.Request{Operation: "read", Path: "data"}, nil, &out)
	must(t, err)
	if out.String() != "content" {
		t.Fatal(out.String())
	}
}
func TestFilesActivityRejectsStalePauseAndDoesNotWakeFromWatch(t *testing.T) {
	f, a, _ := fileFixture(t)
	snapshot, err := f.Store.ArmIdle(ctx, a, time.Nanosecond)
	must(t, err)
	o, fresh, err := f.Store.reserveFile(ctx, a, "crashed-before-start", writeRequest("x", "hello"))
	must(t, err)
	if !fresh {
		t.Fatal("not admitted")
	}
	wantError(t, f.Lifecycle.PauseIdle(ctx, a, snapshot), ErrConflict)
	// An owner crash before dispatch still releases after original-handle absence;
	// it must not launch the upload during recovery.
	must(t, f.Reconcile(ctx, 32))
	o, err = f.Store.FileOperation(ctx, a, o.ID)
	must(t, err)
	if !o.Settled || o.State != "unknown" {
		t.Fatal(o)
	}
	p, err := f.Store.Project(ctx, a)
	must(t, err)
	must(t, f.Lifecycle.Pause(ctx, a, p.Revision))
	drive(t, f.Lifecycle, a.ProjectID)
	_, err = f.Watch(ctx, a, 0)
	wantError(t, err, ErrPending)
	_, err = f.Run(ctx, a, "poll", projectfiles.Request{Operation: "list", Limit: 100}, nil, io.Discard)
	wantError(t, err, ErrPending)
	p, err = f.Store.Project(ctx, a)
	must(t, err)
	if p.State != "paused" {
		t.Fatal("observer woke Project")
	}
	_, err = f.Run(ctx, a, "resume-read", projectfiles.Request{Operation: "read", Path: "x"}, nil, io.Discard)
	wantError(t, err, ErrPending)
	drive(t, f.Lifecycle, a.ProjectID)
	p, err = f.Store.Project(ctx, a)
	must(t, err)
	if p.State != "active" {
		t.Fatal(p)
	}
}
