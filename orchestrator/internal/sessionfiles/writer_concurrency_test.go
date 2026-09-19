package sessionfiles

import (
	"context"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type uploadRepository struct {
	WriterRepository
	mu       sync.Mutex
	mutation workspace.GuestMutation
}

func (r *uploadRepository) BeginGuestWorkspaceMutation(_ context.Context, p workspace.Mutation, c workspace.PreparedContent, _ int) (workspace.GuestMutation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.State = "active"
	r.mutation = workspace.GuestMutation{Mutation: p, Content: c, Reference: sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 1}}
	return r.mutation, nil
}
func (r *uploadRepository) GuestWorkspaceMutationSession(context.Context, string) (string, error) {
	return "session", nil
}
func (r *uploadRepository) LoadGuestWorkspaceMutation(context.Context, string, string) (workspace.GuestMutation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutation, nil
}
func (r *uploadRepository) MarkGuestWorkspaceMutationIssued(context.Context, workspace.GuestMutation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutation.Issued = true
	return nil
}
func (r *uploadRepository) FinishWorkspaceMutation(_ context.Context, p workspace.Mutation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutation.Mutation = p
	return nil
}

type uploadContent struct{}

func (uploadContent) Prepare(context.Context, io.Reader, int64) (workspace.PreparedContent, error) {
	return workspace.PreparedContent{Digest: strings.Repeat("a", 64), Size: 1}, nil
}
func (uploadContent) Open(context.Context, workspace.PreparedContent) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("x")), nil
}

type blockedUpload struct {
	sandbox.Processes
	entered, release chan struct{}
	signals          atomic.Int32
}

func (b *blockedUpload) StartProcess(ctx context.Context, _ sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	close(b.entered)
	return &blockedUploadStream{ctx: ctx, release: b.release}, nil
}
func (b *blockedUpload) SendProcessInput(context.Context, sandbox.ProcessReference, []byte) error {
	return nil
}
func (b *blockedUpload) CloseProcessInput(context.Context, sandbox.ProcessReference) error {
	return nil
}
func (b *blockedUpload) SignalProcess(context.Context, sandbox.ProcessReference, int32) error {
	b.signals.Add(1)
	return nil
}

type blockedUploadStream struct {
	ctx     context.Context
	release chan struct{}
	sent    bool
}

func (s *blockedUploadStream) Next() (sandbox.ProcessFrame, error) {
	if s.sent {
		return sandbox.ProcessFrame{Kind: "end"}, nil
	}
	select {
	case <-s.ctx.Done():
		return sandbox.ProcessFrame{}, s.ctx.Err()
	case <-s.release:
	}
	s.sent = true
	raw, _ := json.Marshal(map[string]any{"path": "file", "size": 1, "sha256": strings.Repeat("a", 64)})
	return sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: raw}, nil
}
func (s *blockedUploadStream) Close() error { return nil }

func TestBackgroundRecoveryCannotTerminateAnActiveUploadRequest(t *testing.T) {
	repo := &uploadRepository{}
	backend := &blockedUpload{entered: make(chan struct{}), release: make(chan struct{})}
	writer := &Writer{Repository: repo, Backend: backend, Content: uploadContent{}}
	finished := make(chan error, 1)
	go func() {
		_, err := writer.WriteSession(context.Background(), "session", "owner", "file", strings.NewReader("x"), 1)
		finished <- err
	}()
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start helper")
	}
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := writer.Recover(deadline, "mutation", "owner"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("background recovery crossed live writer: %v", err)
	}
	if backend.signals.Load() != 0 {
		t.Fatal("recovery killed active helper")
	}
	close(backend.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if file, err := writer.Recover(context.Background(), "mutation", "owner"); err != nil || file.Size != 1 {
		t.Fatalf("post-request recovery: %+v %v", file, err)
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.sessions) != 0 {
		t.Fatal("idle upload locks leaked")
	}
}
