// Package guest invokes the confined guest helper through the process port.
package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
)

const maxReadChunk = 256 << 10

// Files is bound to one accepted placement. A paused environment is activated
// through the environment operation lane before constructing this adapter.
type Files struct {
	Backend   sandbox.Processes
	Reference sandbox.Reference
	Root      string
}

type helperResult struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Digest     string `json:"sha256"`
	ModifiedNS int64  `json:"modified_ns"`
}

func (f Files) validate(name string) error {
	if f.Backend == nil || f.Reference.StoreID == "" || f.Reference.SandboxID == "" || f.Reference.Epoch == 0 {
		return fmt.Errorf("accepted guest placement required")
	}
	if f.Root != "/workspace" && f.Root != "/root/.pi/agent/sessions" {
		return workspace.ErrInvalidPath
	}
	_, err := workspace.CleanPath(name)
	return err
}

func (f Files) Open(ctx context.Context, name string) (io.ReadSeekCloser, workspace.File, error) {
	if err := f.validate(name); err != nil {
		return nil, workspace.File{}, err
	}
	output, err := f.call(ctx, "stat-"+uuid.NewString(), []string{"--operation", "stat", "--path", name}, nil, 4096)
	if err != nil {
		return nil, workspace.File{}, err
	}
	var result helperResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, workspace.File{}, err
	}
	if result.Path != name || result.Size < 0 {
		return nil, workspace.File{}, fmt.Errorf("invalid guest file metadata")
	}
	metadata := workspace.File{Path: name, Size: result.Size, ModifiedAt: time.Unix(0, result.ModifiedNS).UTC()}
	return &rangeReader{files: f, ctx: ctx, name: name, size: result.Size}, metadata, nil
}

// ReplacePrepared consumes already-hashed content named by a durable mutation
// intent. An ambiguous process/input result is never retried here: the caller
// retains its operation fence and reconciles the target digest first.
func (f Files) ReplacePrepared(ctx context.Context, key, name string, size int64, digest string, content io.Reader) (workspace.File, error) {
	result := workspace.File{Path: name, Size: size, Digest: digest}
	if err := f.validate(name); err != nil {
		return result, err
	}
	if key == "" || size < 0 || len(digest) != 64 || content == nil {
		return result, fmt.Errorf("durable mutation key and verified content required")
	}
	output, err := f.call(ctx, key, []string{"--operation", "replace", "--path", name, "--key", key, "--size", strconv.FormatInt(size, 10), "--sha256", digest}, content, 4096)
	if err != nil {
		return result, errors.Join(workspace.ErrUncertain, err)
	}
	var observed helperResult
	if err := json.Unmarshal(output, &observed); err != nil {
		return result, errors.Join(workspace.ErrUncertain, err)
	}
	if observed.Path != name || observed.Size != size || observed.Digest != digest {
		return result, errors.Join(workspace.ErrUncertain, fmt.Errorf("guest mutation result does not match durable intent"))
	}
	return result, nil
}

func (f Files) Sync(ctx context.Context, key, name string) (workspace.File, error) {
	return f.sync(ctx, key, name, "")
}
func (f Files) SyncNative(ctx context.Context, key, name, nativeID string) (workspace.File, error) {
	if nativeID == "" || f.Root != "/root/.pi/agent/sessions" {
		return workspace.File{}, fmt.Errorf("selected native identity and Pi session root required")
	}
	return f.sync(ctx, key, name, nativeID)
}

func (f Files) sync(ctx context.Context, key, name, nativeID string) (workspace.File, error) {
	if err := f.validate(name); err != nil {
		return workspace.File{}, err
	}
	args := []string{"--operation", "sync", "--path", name}
	if nativeID != "" {
		args = append(args, "--native-id", nativeID)
	}
	output, err := f.call(ctx, key, args, nil, 4096)
	if err != nil {
		return workspace.File{}, err
	}
	var result helperResult
	if err := json.Unmarshal(output, &result); err != nil {
		return workspace.File{}, err
	}
	if result.Path != name || result.Size < 0 || len(result.Digest) != 64 {
		return workspace.File{}, fmt.Errorf("invalid guest durability evidence")
	}
	return workspace.File{Path: name, Size: result.Size, Digest: result.Digest}, nil
}

func (f Files) call(ctx context.Context, key string, args []string, input io.Reader, maxOutput int64) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("helper operation key required")
	}
	reference := sandbox.ProcessReference{StoreID: f.Reference.StoreID, SandboxID: f.Reference.SandboxID, Epoch: f.Reference.Epoch, Handle: "helper-" + key}
	arguments := append([]string{"--root", f.Root}, args...)
	stream, err := f.Backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: reference, IdempotencyKey: key, Command: "/usr/local/bin/harness-workspace-helper", Arguments: arguments, Directory: "/workspace", Environment: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/root"}})
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		if !completed {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = f.Backend.SignalProcess(stopCtx, reference, 9)
			cancel()
		}
		_ = stream.Close()
	}()
	if input != nil {
		buffer := make([]byte, maxReadChunk)
		for {
			n, err := input.Read(buffer)
			if n > 0 {
				if err := f.Backend.SendProcessInput(ctx, reference, buffer[:n]); err != nil {
					return nil, err
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	if err := f.Backend.CloseProcessInput(ctx, reference); err != nil && input != nil {
		return nil, err
	}
	var output, diagnostic bytes.Buffer
	for {
		frame, err := stream.Next()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("guest helper ended without terminal result")
			}
			return nil, err
		}
		if frame.Gap || frame.ResetReason != "" {
			return nil, fmt.Errorf("guest helper output gap")
		}
		switch frame.Kind {
		case "data":
			if frame.Stream == "stdout" {
				if int64(output.Len()+len(frame.Data)) > maxOutput {
					return nil, fmt.Errorf("guest helper output exceeded bound")
				}
				output.Write(frame.Data)
			} else if diagnostic.Len() < 4096 {
				remaining := 4096 - diagnostic.Len()
				if len(frame.Data) < remaining {
					remaining = len(frame.Data)
				}
				diagnostic.Write(frame.Data[:remaining])
			}
		case "end":
			completed = true
			if frame.ExitCode != 0 {
				return nil, fmt.Errorf("guest helper failed (%d): %s", frame.ExitCode, diagnostic.String())
			}
			return output.Bytes(), nil
		}
	}
}

type rangeReader struct {
	files        Files
	ctx          context.Context
	name         string
	size, offset int64
	closed       bool
}

func (r *rangeReader) Read(destination []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(destination) == 0 {
		return 0, nil
	}
	if r.offset >= r.size {
		return 0, io.EOF
	}
	length := int64(len(destination))
	if length > maxReadChunk {
		length = maxReadChunk
	}
	if length > r.size-r.offset {
		length = r.size - r.offset
	}
	output, err := r.files.call(r.ctx, "read-"+uuid.NewString(), []string{"--operation", "read", "--path", r.name, "--offset", strconv.FormatInt(r.offset, 10), "--length", strconv.FormatInt(length, 10)}, nil, length)
	if err != nil {
		return 0, err
	}
	if int64(len(output)) != length {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(destination, output)
	r.offset += int64(n)
	return n, nil
}
func (r *rangeReader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	position := offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		position += r.offset
	case io.SeekEnd:
		position += r.size
	default:
		return r.offset, fmt.Errorf("invalid seek origin")
	}
	if position < 0 {
		return r.offset, fmt.Errorf("negative seek offset")
	}
	r.offset = position
	return position, nil
}
func (r *rangeReader) Close() error { r.closed = true; return nil }

var _ workspace.Reader = Files{}
