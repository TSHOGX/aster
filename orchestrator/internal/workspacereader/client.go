// Package workspacereader carries read-only workspace requests over one retained
// guest process stream. It owns no authorization or durable mutation outcome.
package workspacereader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

type RemoteError struct{ Code string }

func (e *RemoteError) Error() string { return "workspace reader: " + e.Code }

var ErrClosed = errors.New("workspace reader connection closed")
var ErrCapacity = errors.New("workspace reader capacity exhausted")

type pending struct {
	frames chan projectfiles.ReadFrame
	done   chan struct{}
	err    error
}
type Client struct {
	stream   sandbox.ProcessStream
	input    func(context.Context, []byte) error
	mu       sync.Mutex
	send     chan struct{}
	pending  map[string]*pending
	closed   error
	ready    chan struct{}
	finished chan struct{}
	close    sync.Once
}

func New(ctx context.Context, stream sandbox.ProcessStream, input func(context.Context, []byte) error) (*Client, error) {
	c := &Client{stream: stream, input: input, pending: map[string]*pending{}, send: make(chan struct{}, 1), ready: make(chan struct{}), finished: make(chan struct{})}
	go c.receive()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	select {
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	case <-timeout.C:
		c.Close()
		return nil, errors.New("workspace reader handshake timed out")
	case <-c.ready:
	case <-c.finished:
	}
	c.mu.Lock()
	err := c.closed
	c.mu.Unlock()
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *Client) Close() error {
	var err error
	c.close.Do(func() { c.fail(ErrClosed); err = c.stream.Close(); <-c.finished })
	return err
}
func (c *Client) Alive() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed == nil }
func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed != nil {
		return
	}
	c.closed = err
	for id, p := range c.pending {
		p.err = err
		close(p.done)
		delete(c.pending, id)
	}
}
func (c *Client) receive() {
	defer close(c.finished)
	reader := &processOutput{stream: c.stream}
	var greeting projectfiles.ReadFrame
	if err := projectfiles.ReadWire(reader, projectfiles.ReaderFrameBytes, &greeting); err != nil {
		c.fail(err)
		return
	}
	if greeting.Version != projectfiles.ReaderProtocol || greeting.Kind != "ready" || greeting.ID != "" {
		c.fail(errors.New("incompatible workspace reader protocol"))
		return
	}
	close(c.ready)
	for {
		var frame projectfiles.ReadFrame
		if err := projectfiles.ReadWire(reader, projectfiles.ReaderFrameBytes, &frame); err != nil {
			c.fail(err)
			return
		}
		if frame.Version != projectfiles.ReaderProtocol || frame.ID == "" || len(frame.ID) > 128 {
			c.fail(errors.New("invalid workspace reader response identity"))
			return
		}
		switch frame.Kind {
		case "metadata", "data", "complete", "error":
		default:
			c.fail(errors.New("invalid workspace reader response kind"))
			return
		}
		c.mu.Lock()
		p := c.pending[frame.ID]
		if p != nil {
			select {
			case p.frames <- frame:
			default:
				p.err = ErrCapacity
				close(p.done)
				delete(c.pending, frame.ID)
			}
		}
		c.mu.Unlock()
	}
}
func (c *Client) write(ctx context.Context, request projectfiles.ReadRequest) error {
	select {
	case c.send <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.send }()
	var body bytes.Buffer
	if err := projectfiles.WriteWire(&body, projectfiles.ReaderRequestBytes, request); err != nil {
		return err
	}
	if err := c.input(ctx, body.Bytes()); err != nil {
		// A failed send may have written a prefix. No subsequent request may append
		// to that uncertain frame; the owner retires this connection and helper.
		c.fail(err)
		_ = c.stream.Close()
		return err
	}
	return nil
}

func (c *Client) Call(ctx context.Context, request projectfiles.ReadRequest, consume func(projectfiles.ReadFrame) error) error {
	request.ID = uuid.NewString()
	request.Version = projectfiles.ReaderProtocol
	if request.Validate() != nil || request.Operation == "cancel" {
		return projectfiles.ErrPath
	}
	p := &pending{frames: make(chan projectfiles.ReadFrame, 8), done: make(chan struct{})}
	c.mu.Lock()
	if c.closed != nil {
		err := c.closed
		c.mu.Unlock()
		return err
	}
	if len(c.pending) >= 2 {
		c.mu.Unlock()
		return ErrCapacity
	}
	c.pending[request.ID] = p
	c.mu.Unlock()
	complete := false
	defer func() {
		c.mu.Lock()
		delete(c.pending, request.ID)
		alive := c.closed == nil
		c.mu.Unlock()
		if !complete && alive {
			cancel, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			_ = c.write(cancel, projectfiles.ReadRequest{Version: projectfiles.ReaderProtocol, ID: request.ID, Operation: "cancel"})
		}
	}()
	sendCtx, done := context.WithTimeout(ctx, 5*time.Second)
	err := c.write(sendCtx, request)
	done()
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return p.err
		case frame := <-p.frames:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if frame.Kind == "error" {
				complete = true
				return &RemoteError{Code: frame.Code}
			}
			if err := consume(frame); err != nil {
				return err
			}
			if frame.Kind == "complete" {
				complete = true
				return nil
			}
		}
	}
}

type processOutput struct {
	stream sandbox.ProcessStream
	buffer []byte
}

func (p *processOutput) Read(out []byte) (int, error) {
	for len(p.buffer) == 0 {
		f, err := p.stream.Next()
		if err != nil {
			return 0, err
		}
		if f.Gap || f.ResetReason != "" {
			return 0, errors.New("workspace reader transport gap")
		}
		switch f.Kind {
		case "data":
			if f.Stream != "stderr" {
				p.buffer = f.Data
			}
		case "end":
			if f.ExitCode != 0 {
				return 0, fmt.Errorf("workspace reader exited: %d", f.ExitCode)
			}
			return 0, io.EOF
		}
	}
	n := copy(out, p.buffer)
	p.buffer = p.buffer[n:]
	return n, nil
}
