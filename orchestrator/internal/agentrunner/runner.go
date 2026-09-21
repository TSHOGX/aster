// Package agentrunner hosts native Pi and the durable application protocol. Its
// lifetime is a Session incarnation, independent of browser or host attachment.
package agentrunner

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/agentjournal"
	"harness-platform/orchestrator/internal/agentwire"
)

type Config struct {
	Session, Incarnation, StateRoot, Workspace, PiBinary string
	PiArgs                                               []string
	Environment                                          []string
	JournalBytes                                         int64
	// ServiceCredential is this incarnation's application token for the Project's
	// declared service audience. It is written into Pi's authored provider
	// configuration before Pi starts, so no native command carries a secret and no
	// per-turn rewrite races a running turn. It is not isolation from a sibling
	// Session: guest root can read either Session's configuration.
	ServiceCredential string
}
type Runner struct {
	config       Config
	journal      *agentjournal.Journal
	native       io.WriteCloser
	pi           *exec.Cmd
	mu           sync.Mutex
	active, turn string
	commands     map[string]agentjournal.Command
	wake         chan struct{}
	responses    chan agentwire.Frame
	syncs        chan agentwire.Frame
	fatal        chan error
}

func (r *Runner) notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *Runner) fail(err error) {
	select {
	case r.fatal <- err:
	default:
	}
}
func (r *Runner) response(f agentwire.Frame) {
	select {
	case r.responses <- f:
	default:
		r.fail(errors.New("Agent control response buffer exhausted"))
	}
}

func Run(ctx context.Context, c Config, input io.Reader, output, diagnostics io.Writer) error {
	if !validIdentity(c.Session) || !validIdentity(c.Incarnation) || !filepath.IsAbs(c.StateRoot) || !filepath.IsAbs(c.Workspace) || c.PiBinary == "" {
		return errors.New("invalid runner configuration")
	}
	if c.JournalBytes == 0 {
		c.JournalBytes = 64 << 20
	}
	root := filepath.Join(c.StateRoot, "sessions", c.Session)
	nativeRoot := filepath.Join(root, "native")
	if err := os.MkdirAll(nativeRoot, 0700); err != nil {
		return err
	}
	j, err := agentjournal.Open(ctx, filepath.Join(root, "incarnations", c.Incarnation, "journal.db"), c.Session, c.Incarnation, c.JournalBytes)
	if err != nil {
		return err
	}
	defer j.Close()
	unresolved, err := j.Unresolved(ctx)
	if err != nil {
		return err
	}
	if len(unresolved) > 0 {
		return agentjournal.ErrUnknown
	}
	if err = becomeSubreaper(); err != nil {
		return err
	}
	if err = materializeCredential(root, c.ServiceCredential); err != nil {
		return err
	}
	nativeFile := filepath.Join(nativeRoot, "session.jsonl")
	args := c.PiArgs
	if args == nil {
		args = []string{"--mode", "rpc", "--session", nativeFile, "--session-dir", nativeRoot, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes"}
	}
	cmd := exec.Command(c.PiBinary, args...)
	cmd.Dir = c.Workspace
	cmd.Env = append(os.Environ(), c.Environment...)
	cmd.Env = append(cmd.Env, "PI_CODING_AGENT_DIR="+root, "PI_CODING_AGENT_SESSION_DIR="+nativeRoot, "PI_OFFLINE=1", "PI_SKIP_VERSION_CHECK=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdout.Close()
	defer stdoutWriter.Close()
	cmd.Stdout = stdoutWriter
	cmd.Stderr = &boundedDiagnostic{writer: diagnostics, remaining: 64 << 10}
	if err = cmd.Start(); err != nil {
		return err
	}
	stdoutWriter.Close()
	runctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &Runner{config: c, journal: j, native: stdin, pi: cmd, commands: map[string]agentjournal.Command{}, wake: make(chan struct{}, 1), responses: make(chan agentwire.Frame, 32), syncs: make(chan agentwire.Frame, 8), fatal: make(chan error, 1)}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); r.nativeOutput(runctx, stdout, nativeFile) }()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	go r.emit(runctx, output)
	go r.input(runctx, input)
	var outcome error
	select {
	case <-ctx.Done():
		outcome = ctx.Err()
	case outcome = <-r.fatal:
	case outcome = <-wait:
		if outcome == nil {
			outcome = errors.New("native agent exited")
		}
		wait = nil
	}
	cancel()
	_ = stdin.Close()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	cleanupErr := settleDescendants(cmd.Process.Pid, false)
	if wait != nil {
		select {
		case <-wait:
		case <-time.After(3 * time.Second):
			cleanupErr = errors.Join(cleanupErr, errors.New("native wait timed out"))
		}
	}
	_ = stdout.Close()
	workers.Wait()
	if cleanupErr == nil && errors.Is(outcome, context.Canceled) {
		pending, err := j.Unresolved(context.Background())
		if err == nil && len(pending) == 0 {
			return nil
		}
		cleanupErr = errors.Join(err, agentjournal.ErrUnknown)
	}
	return errors.Join(outcome, cleanupErr)
}

type boundedDiagnostic struct {
	mu        sync.Mutex
	writer    io.Writer
	remaining int
}

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.remaining > 0 {
		keep := n
		if keep > b.remaining {
			keep = b.remaining
		}
		_, _ = b.writer.Write(p[:keep])
		b.remaining -= keep
	}
	return n, nil
}

func (r *Runner) input(ctx context.Context, input io.Reader) {
	decoder := agentwire.NewDecoder(input)
	for {
		f, err := decoder.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.fail(err)
			}
			return
		}
		if f.Session != r.config.Session || f.Incarnation != r.config.Incarnation {
			r.response(agentwire.Frame{Kind: "error", Owner: f.Owner, Error: "wrong runner identity"})
			continue
		}
		switch f.Kind {
		case "sync":
			r.mu.Lock()
			err = r.journal.Sync(ctx, f.Owner, f.After)
			r.mu.Unlock()
			if err == nil {
				select {
				case r.syncs <- f:
				case <-ctx.Done():
					return
				}
				r.notify()
			}
		case "ack":
			err = r.journal.Ack(ctx, f.Owner, f.Sequence)
		case "command":
			err = r.command(ctx, f)
		default:
			err = agentwire.ErrFrame
		}
		if err != nil {
			r.response(agentwire.Frame{Kind: "error", Owner: f.Owner, CommandID: f.CommandID, Error: err.Error()})
			if errors.Is(err, agentjournal.ErrFull) {
				r.fail(err)
				return
			}
		}
	}
}
func (r *Runner) command(ctx context.Context, f agentwire.Frame) error {
	var native map[string]json.RawMessage
	if err := json.Unmarshal(f.Payload, &native); err != nil {
		return agentwire.ErrFrame
	}
	var kind string
	if err := json.Unmarshal(native["type"], &kind); err != nil {
		return agentwire.ErrFrame
	}
	switch kind {
	case "prompt", "steer", "abort", "get_state":
	default:
		return errors.New("unsupported native command")
	}
	if (kind == "prompt" || kind == "steer") && f.TurnID == "" {
		return agentwire.ErrFrame
	}
	// Restrict this boundary to native RPC commands, not arbitrary configuration
	// mutation or environment rewriting at command dispatch.
	for key := range native {
		if key != "type" && key != "message" {
			return agentwire.ErrFrame
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, duplicate, err := r.journal.Receive(ctx, f.Owner, agentjournal.Command{ID: f.CommandID, TurnID: f.TurnID, Kind: kind, Payload: f.Payload})
	if err != nil {
		return err
	}
	if duplicate && c.State != "received" {
		r.response(agentwire.Frame{Kind: "command_status", Owner: f.Owner, CommandID: c.ID, TurnID: c.TurnID, State: c.State, Payload: c.Result})
		return nil
	}
	if kind == "prompt" && r.active != "" {
		_, err = r.journal.Result(ctx, c.ID, "rejected", json.RawMessage(`{"error":"Session is busy"}`))
		r.notify()
		return err
	}
	if (kind == "steer" || kind == "abort") && r.active == "" {
		_, err = r.journal.Result(ctx, c.ID, "rejected", json.RawMessage(`{"error":"no running turn"}`))
		r.notify()
		return err
	}
	if err = r.journal.Dispatch(ctx, f.Owner, c.ID); err != nil {
		return err
	}
	native["id"], _ = json.Marshal(c.ID)
	raw, err := json.Marshal(native)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	r.commands[c.ID] = c
	if kind == "prompt" {
		r.active, r.turn = c.ID, c.TurnID
	}
	n, err := r.native.Write(raw)
	if err != nil || n != len(raw) {
		_, _ = r.journal.Result(ctx, c.ID, "unknown", json.RawMessage(`{"error":"native stdin outcome unknown"}`))
		r.notify()
		r.fail(agentjournal.ErrUnknown)
		return agentjournal.ErrUnknown
	}
	r.response(agentwire.Frame{Kind: "command_status", Owner: f.Owner, CommandID: c.ID, TurnID: c.TurnID, State: "dispatching"})
	return nil
}
func (r *Runner) emit(ctx context.Context, output io.Writer) {
	var owner, cursor uint64
	send := func(f agentwire.Frame) error {
		f.Session, f.Incarnation = r.config.Session, r.config.Incarnation
		if f.Owner == 0 {
			f.Owner = owner
		}
		return agentwire.Encode(output, f)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-r.syncs:
			owner, cursor = f.Owner, f.After
			if err := send(agentwire.Frame{Kind: "synced", Owner: owner, After: cursor}); err != nil {
				return
			}
		case f := <-r.responses:
			if err := send(f); err != nil {
				return
			}
		case <-r.wake:
		}
		if owner == 0 {
			continue
		}
		records, err := r.journal.Records(ctx, owner, cursor, 32)
		if errors.Is(err, agentjournal.ErrStale) {
			continue
		}
		if err != nil {
			r.response(agentwire.Frame{Kind: "error", Owner: owner, Error: err.Error()})
			continue
		}
		for _, record := range records {
			if err := send(agentwire.Frame{Kind: "record", Owner: owner, Sequence: record.Sequence, CommandID: record.CommandID, TurnID: record.TurnID, State: record.Kind, Payload: record.Payload}); err != nil {
				return
			}
			cursor = record.Sequence
		}
		if len(records) > 0 {
			r.notify()
		}
	}
}
func (r *Runner) nativeOutput(ctx context.Context, output io.Reader, nativeFile string) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), agentwire.MaxFrame-1024)
	for scanner.Scan() {
		raw := append(json.RawMessage{}, scanner.Bytes()...)
		var event struct {
			Type, ID string
			Success  bool
			Messages []struct {
				Role       string
				StopReason string
			}
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			r.fail(errors.Join(agentwire.ErrFrame, err))
			return
		}
		r.mu.Lock()
		command, turn := r.active, r.turn
		if event.Type == "response" {
			c, ok := r.commands[event.ID]
			if !ok {
				r.mu.Unlock()
				r.fail(errors.New("uncorrelated native response"))
				return
			}
			state := "applied"
			if !event.Success {
				state = "rejected"
			}
			if _, err := r.journal.Result(ctx, c.ID, state, raw); err != nil {
				r.mu.Unlock()
				r.fail(err)
				return
			}
			if c.Kind != "prompt" || !event.Success {
				delete(r.commands, c.ID)
			}
			if c.Kind == "prompt" && !event.Success {
				r.active, r.turn = "", ""
			}
		}
		if _, err := r.journal.Append(ctx, agentjournal.Record{CommandID: command, TurnID: turn, Kind: "native", Payload: raw}); err != nil {
			r.mu.Unlock()
			r.fail(err)
			return
		}
		if event.Type == "agent_end" && command != "" {
			if err := settleDescendants(r.pi.Process.Pid, true); err != nil {
				r.mu.Unlock()
				r.fail(err)
				return
			}
			capture, err := captureNative(nativeFile, filepath.Join(filepath.Dir(nativeFile), "captures"), command)
			if err != nil {
				r.mu.Unlock()
				r.fail(err)
				return
			}
			capture.Outcome = "completed"
			for _, message := range event.Messages {
				if message.Role == "assistant" {
					switch message.StopReason {
					case "aborted":
						capture.Outcome = "canceled"
					case "error":
						capture.Outcome = "failed"
					}
				}
			}
			payload, _ := json.Marshal(capture)
			if _, err = r.journal.Result(ctx, command, "completed", payload); err != nil {
				r.mu.Unlock()
				r.fail(err)
				return
			}
			delete(r.commands, command)
			r.active, r.turn = "", ""
		}
		r.mu.Unlock()
		r.notify()
	}
	if err := scanner.Err(); err != nil {
		r.fail(err)
	}
}

type Capture struct {
	Outcome string `json:"outcome"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
}

func captureNative(source, directory, command string) (Capture, error) {
	if !validIdentity(command) {
		return Capture{}, errors.New("invalid capture command identity")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return Capture{}, err
	}
	src, err := os.Open(source)
	if err != nil {
		return Capture{}, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return Capture{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return Capture{}, errors.New("native capture size/type limit")
	}
	// Pi has reached agent_end. No next prompt is dispatched until this immutable
	// per-Session copy is synced. Workspace and sibling Sessions remain concurrent.
	dst, err := os.CreateTemp(directory, "capture-*.tmp")
	if err != nil {
		return Capture{}, err
	}
	defer os.Remove(dst.Name())
	defer dst.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, 64<<20+1))
	if err != nil {
		return Capture{}, err
	}
	if n > 64<<20 {
		return Capture{}, errors.New("native capture grew past limit")
	}
	if err = dst.Sync(); err != nil {
		return Capture{}, err
	}
	if err = dst.Close(); err != nil {
		return Capture{}, err
	}
	path := filepath.Join(directory, command+".jsonl")
	if err = os.Rename(dst.Name(), path); err != nil {
		return Capture{}, err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return Capture{}, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return Capture{}, err
	}
	return Capture{Path: path, Digest: fmt.Sprintf("sha256:%x", h.Sum(nil)), Size: n}, nil
}
