package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
)

type helperBackend struct {
	sandbox.Processes
	frames     []sandbox.ProcessFrame
	starts     []sandbox.StartProcessRequest
	inputs     []byte
	signals    int
	inputError bool
	closed     bool
}

func (b *helperBackend) StartProcess(_ context.Context, request sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	b.starts = append(b.starts, request)
	return &helperStream{backend: b}, nil
}
func (b *helperBackend) SendProcessInput(_ context.Context, _ sandbox.ProcessReference, data []byte) error {
	b.inputs = append(b.inputs, data...)
	if b.inputError {
		return errors.New("ambiguous partial input")
	}
	return nil
}
func (b *helperBackend) CloseProcessInput(context.Context, sandbox.ProcessReference) error {
	return nil
}
func (b *helperBackend) SignalProcess(context.Context, sandbox.ProcessReference, int32) error {
	b.signals++
	return nil
}

type helperStream struct{ backend *helperBackend }

func (s *helperStream) Next() (sandbox.ProcessFrame, error) {
	if len(s.backend.frames) == 0 {
		return sandbox.ProcessFrame{}, io.EOF
	}
	frame := s.backend.frames[0]
	s.backend.frames = s.backend.frames[1:]
	return frame, nil
}
func (s *helperStream) Close() error { s.backend.closed = true; return nil }
func fixture() *Files {
	return &Files{Reference: sandbox.Reference{StoreID: "engine", SandboxID: "sandbox", Epoch: 7}, Root: "/workspace"}
}

func TestRemoteReplacementPreservesIdentityAndVerifiesHelperEvidence(t *testing.T) {
	files := fixture()
	content := "synthetic content"
	hash := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(hash[:])
	backend := &helperBackend{frames: []sandbox.ProcessFrame{{Kind: "data", Stream: "stdout", Data: []byte(`{"path":"file","size":17,"sha256":"` + digest + `"}`)}, {Kind: "end"}}}
	files.Backend = backend
	result, err := files.ReplacePrepared(context.Background(), "durable-operation", "file", 17, digest, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if result.Digest != digest || string(backend.inputs) != content || len(backend.starts) != 1 {
		t.Fatalf("replacement: %+v %+v", result, backend.starts)
	}
	request := backend.starts[0]
	if request.IdempotencyKey != "durable-operation" || request.Handle != "helper-durable-operation" || request.Epoch != 7 || request.Command != "/usr/local/bin/harness-workspace-helper" {
		t.Fatalf("helper lost scoped intent: %+v", request)
	}
	if backend.signals != 0 || !backend.closed {
		t.Fatal("successful helper was killed or stream leaked")
	}
}
func TestAmbiguousRemoteInputIsNotRetriedAndCannotReleaseMutation(t *testing.T) {
	files := fixture()
	backend := &helperBackend{inputError: true}
	files.Backend = backend
	_, err := files.ReplacePrepared(context.Background(), "operation", "file", 7, strings.Repeat("a", 64), strings.NewReader("content"))
	if !errors.Is(err, workspace.ErrUncertain) || len(backend.starts) != 1 || backend.signals != 1 || !backend.closed {
		t.Fatalf("ambiguous write mishandled: %v %+v", err, backend)
	}
}
func TestRemoteReadRejectsTruncatedProcessTail(t *testing.T) {
	files := fixture()
	backend := &helperBackend{frames: []sandbox.ProcessFrame{{Kind: "data", Stream: "stdout", Data: []byte(`{"path":"file","size":100,"modified_ns":1}`)}, {Kind: "end"}, {Kind: "data", Stream: "stdout", Data: []byte("tail")}, {Kind: "end"}}}
	files.Backend = backend
	reader, _, err := files.Open(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Read(make([]byte, 100)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short transport tail accepted as file: %v", err)
	}
}
func TestRemoteHelperGapFailsEvenWhenBytesLookComplete(t *testing.T) {
	files := fixture()
	backend := &helperBackend{frames: []sandbox.ProcessFrame{{Kind: "data", Stream: "stdout", Data: []byte(`{"path":"file","size":0,"modified_ns":1}`), Gap: true}}}
	files.Backend = backend
	if _, _, err := files.Open(context.Background(), "file"); err == nil {
		t.Fatal("helper gap ignored")
	}
	if backend.signals != 1 {
		t.Fatal("unsettled helper process left running")
	}
}
