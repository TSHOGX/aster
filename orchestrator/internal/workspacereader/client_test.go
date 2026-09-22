package workspacereader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

type pipeStream struct {
	input *io.PipeReader
	once  sync.Once
}

func (p *pipeStream) Next() (sandbox.ProcessFrame, error) {
	buf := make([]byte, 701)
	n, err := p.input.Read(buf)
	return sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: buf[:n]}, err
}
func (p *pipeStream) Close() error { return p.input.Close() }
func clientFixture(t *testing.T) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	in, input := io.Pipe()
	output, out := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- projectfiles.ServeReader(ctx, root, in, out, time.Second); out.Close() }()
	stream := &pipeStream{input: output}
	client, err := New(ctx, stream, func(ctx context.Context, data []byte) error {
		stop := context.AfterFunc(ctx, func() { input.CloseWithError(ctx.Err()) })
		defer stop()
		_, err := input.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		cancel()
		input.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("reader server leaked")
		}
	})
	return client, root
}
func TestClientReusableReadAndIndependentCancellation(t *testing.T) {
	c, root := clientFixture(t)
	data := bytes.Repeat([]byte("世界\n"), 1<<18)
	if err := os.WriteFile(filepath.Join(root, "data"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var digest string
	if err := c.Call(context.Background(), projectfiles.ReadRequest{Operation: "read", Path: "data"}, func(frame projectfiles.ReadFrame) error {
		if frame.Kind == "data" {
			if frame.Offset != int64(output.Len()) {
				t.Fatal("offset mismatch")
			}
			output.Write(frame.Data)
		}
		if frame.Kind == "complete" {
			digest = frame.Digest
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if !bytes.Equal(output.Bytes(), data) || digest != hex.EncodeToString(sum[:]) {
		t.Fatal("corrupted capture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- c.Call(ctx, projectfiles.ReadRequest{Operation: "read", Path: "data"}, func(frame projectfiles.ReadFrame) error {
			if frame.Kind == "metadata" {
				close(started)
				<-release
			}
			return nil
		})
	}()
	<-started
	err := c.Call(context.Background(), projectfiles.ReadRequest{Operation: "stat", Path: "data"}, func(frame projectfiles.ReadFrame) error {
		if frame.Kind != "complete" || frame.Metadata.Size != int64(len(data)) {
			t.Error(frame.Kind)
		}
		return nil
	})
	if err != nil {
		t.Fatal("sibling stalled behind slow read", err)
	}
	cancel()
	close(release)
	if err = <-result; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	for n := 0; n < 50; n++ {
		if err = c.Call(context.Background(), projectfiles.ReadRequest{Operation: "stat", Path: "data"}, func(projectfiles.ReadFrame) error { return nil }); err != nil {
			t.Fatal(n, err)
		}
	}
}

func TestClientProtocolFailureFencesPendingRequests(t *testing.T) {
	in, out := io.Pipe()
	stream := &pipeStream{input: in}
	go func() {
		_ = projectfiles.WriteWire(out, projectfiles.ReaderFrameBytes, projectfiles.ReadFrame{Version: 1, Kind: "ready"})
		out.Close()
	}()
	c, err := New(context.Background(), stream, func(context.Context, []byte) error { return nil })
	if err != nil {
		return
	} // EOF can race the ready handshake; both outcomes are closed.
	defer c.Close()
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = c.Call(deadline, projectfiles.ReadRequest{Operation: "stat", Path: "x"}, func(projectfiles.ReadFrame) error { return nil }); err == nil {
		t.Fatal("dead helper supplied success")
	}
	if c.Alive() {
		t.Fatal("ended helper remained usable")
	}
}
