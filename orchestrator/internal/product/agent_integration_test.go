//go:build khoraintegration

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

type runnerObserver struct {
	frames chan agentwire.Frame
	errors chan error
	ref    sandbox.ProcessReference
	stop   context.CancelFunc
}

func observeRunner(ctx context.Context, stream sandbox.ProcessStream, ref sandbox.ProcessReference) *runnerObserver {
	ctx, cancel := context.WithCancel(ctx)
	o := &runnerObserver{frames: make(chan agentwire.Frame, 256), errors: make(chan error, 2), ref: ref, stop: cancel}
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		defer stream.Close()
		for {
			f, err := stream.Next()
			if err != nil {
				writer.CloseWithError(err)
				return
			}
			if f.Kind == "end" {
				writer.CloseWithError(fmt.Errorf("runner exit %d", f.ExitCode))
				return
			}
			if f.Kind == "data" && f.Stream != "stderr" {
				if _, err = writer.Write(f.Data); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer reader.Close()
		d := agentwire.NewDecoder(reader)
		for {
			f, err := d.Next()
			if err != nil {
				select {
				case o.errors <- err:
				default:
				}
				return
			}
			select {
			case o.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return o
}
func runnerWait(t *testing.T, o *runnerObserver, match func(agentwire.Frame) bool) []agentwire.Frame {
	t.Helper()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	var seen []agentwire.Frame
	for {
		select {
		case f := <-o.frames:
			seen = append(seen, f)
			if match(f) {
				return seen
			}
		case err := <-o.errors:
			t.Fatal(err)
		case <-timer.C:
			t.Fatalf("runner wait timeout, frames=%+v", seen)
		}
	}
}
func TestGoAgentRunnersOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated endpoint required")
	}
	runctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	api, err := sdk.New(endpoint)
	must(t, err)
	for i := 0; i < 100; i++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	s := testStore(t)
	l := &Lifecycle{VerifyRuntime: backend.VerifyRuntime, Store: s, Backend: backend, MaxProjects: 1}
	scope := Scope{"owner", "agents"}
	_, err = s.CreateProject(runctx, scope.Actor, scope.ProjectID, "Agents")
	must(t, err)
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "agent-runtime"
	profile.MemoryBytes = 2 << 30
	profile.DiskBytes = 256 << 20
	fixtureEnvironment(t, s, profile)
	must(t, l.EnsureActive(runctx, scope, profile.Bindings()))
	drive(t, l, scope.ProjectID)
	r, err := l.ActiveResources(runctx, scope)
	must(t, err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		p, e := s.Project(c, scope)
		if e == nil {
			_ = s.DeleteProject(c, scope, p.Revision)
			for i := 0; i < 8; i++ {
				_ = l.Reconcile(c, scope.ProjectID)
			}
		}
	})
	for _, name := range []string{"agent-runner", "runtime-fixture"} {
		file, err := os.Open(os.Getenv("PROJECT_TEST_BIN_DIR") + "/" + name)
		must(t, err)
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/tmp/"+name, file))
		file.Close()
	}
	serial := 0
	shell := func(command string) string {
		t.Helper()
		serial++
		handle := fmt.Sprintf("setup-%d", serial)
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
		must(t, err)
		defer stream.Close()
		var b strings.Builder
		for {
			f, err := stream.Next()
			must(t, err)
			b.Write(f.Data)
			if f.Kind == "end" {
				if f.ExitCode != 0 {
					t.Fatal(command, f.ExitCode, b.String())
				}
				return b.String()
			}
		}
	}
	shell("chmod 755 /tmp/agent-runner /tmp/runtime-fixture; mkdir -p /runtime/sessions/a /runtime/sessions/b")
	modelRef := sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: "model"}
	model, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: modelRef, IdempotencyKey: "model", Command: "/tmp/runtime-fixture", Arguments: []string{"model"}, Directory: "/workspace"})
	must(t, err)
	defer model.Close()
	for {
		f, err := model.Next()
		must(t, err)
		if strings.Contains(string(f.Data), "model-ready") {
			break
		}
	}
	models := `{"providers":{"local":{"baseUrl":"http://127.0.0.1:18889","api":"anthropic-messages","apiKey":"fixture-only","models":[{"id":"local-model","name":"Local model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`
	observers := map[string]*runnerObserver{}
	start := func(session, incarnation, pi string, budget ...int64) *runnerObserver {
		t.Helper()
		ref := sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: session + "-" + incarnation}
		args := []string{"--session", session, "--incarnation", incarnation}
		if len(budget) > 0 {
			args = append(args, "--journal-bytes", fmt.Sprint(budget[0]))
		}
		if pi != "" {
			args = append(args, "--pi", pi)
		}
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: ref.Handle, Command: "/tmp/agent-runner", Arguments: args, Directory: "/workspace", Environment: []string{"PATH=/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/root"}})
		must(t, err)
		o := observeRunner(runctx, stream, ref)
		t.Cleanup(o.stop)
		return o
	}
	send := func(o *runnerObserver, f agentwire.Frame) {
		t.Helper()
		f.Session = strings.Split(o.ref.Handle, "-")[0]
		f.Incarnation = strings.TrimPrefix(o.ref.Handle, f.Session+"-")
		if f.Owner == 0 {
			f.Owner = 1
		}
		var b bytes.Buffer
		must(t, agentwire.Encode(&b, f))
		must(t, backend.SendProcessInput(runctx, o.ref, b.Bytes()))
	}
	for _, id := range []string{"a", "b"} {
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/runtime/sessions/"+id+"/models.json", strings.NewReader(models)))
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/runtime/sessions/"+id+"/settings.json", strings.NewReader(`{"defaultProvider":"local","defaultModel":"local-model","compaction":{"enabled":false}}`)))
		observers[id] = start(id, "one", "")
		send(observers[id], agentwire.Frame{Kind: "sync"})
		runnerWait(t, observers[id], func(f agentwire.Frame) bool { return f.Kind == "synced" })
	}
	for _, id := range []string{"a", "b"} {
		payload, _ := json.Marshal(map[string]string{"type": "prompt", "message": "unique-" + id})
		send(observers[id], agentwire.Frame{Kind: "command", CommandID: "prompt-" + id, TurnID: "turn-" + id, Payload: payload})
	}
	for _, id := range []string{"a", "b"} {
		seen := runnerWait(t, observers[id], func(f agentwire.Frame) bool { return f.State == "command.completed" })
		raw, _ := json.Marshal(seen)
		if !strings.Contains(string(raw), "deterministic-local-reply") {
			t.Fatal("missing native reply", string(raw))
		}
		if strings.Contains(string(raw), "unique-"+map[string]string{"a": "b", "b": "a"}[id]) {
			t.Fatal("crossed conversation")
		}
		last := seen[len(seen)-1].Sequence
		// Drop every transport record and explicitly replay the runner's durable
		// journal under a new owner, independent of engine replay-ring retention.
		send(observers[id], agentwire.Frame{Kind: "sync", Owner: 2, After: 0})
		replay := runnerWait(t, observers[id], func(f agentwire.Frame) bool { return f.Kind == "record" && f.Sequence == last && f.Owner == 2 })
		sequence := uint64(0)
		for _, f := range replay {
			if f.Kind == "record" {
				sequence++
				if f.Sequence != sequence {
					t.Fatal("journal replay gap", f)
				}
			}
		}
		if sequence != last {
			t.Fatal(sequence, last)
		}
		send(observers[id], agentwire.Frame{Kind: "ack", Owner: 2, Sequence: last})
		send(observers[id], agentwire.Frame{Kind: "command", Owner: 1, CommandID: "stale", Payload: json.RawMessage(`{"type":"get_state"}`)})
		runnerWait(t, observers[id], func(f agentwire.Frame) bool { return f.Kind == "error" && strings.Contains(f.Error, "stale") })
	}
	t.Log("two real Pi runners overlapped, captured distinct native history, replayed journal after discarded transport and rejected stale owner")
	must(t, backend.SignalProcess(runctx, observers["a"].ref, 15))
	// A's runner cleans only its own descendants. B remains able to run a turn.
	send(observers["b"], agentwire.Frame{Kind: "command", Owner: 2, CommandID: "next-b", TurnID: "next-turn-b", Payload: json.RawMessage(`{"type":"prompt","message":"still-b"}`)})
	runnerWait(t, observers["b"], func(f agentwire.Frame) bool { return f.State == "command.completed" && f.CommandID == "next-b" })
	observers["a"] = start("a", "two", "")
	send(observers["a"], agentwire.Frame{Kind: "sync"})
	runnerWait(t, observers["a"], func(f agentwire.Frame) bool { return f.Kind == "synced" })
	send(observers["a"], agentwire.Frame{Kind: "command", CommandID: "state-a", Payload: json.RawMessage(`{"type":"get_state"}`)})
	state := runnerWait(t, observers["a"], func(f agentwire.Frame) bool { return f.State == "command.applied" && f.CommandID == "state-a" })
	raw, _ := json.Marshal(state)
	if !strings.Contains(string(raw), "session.jsonl") {
		t.Fatal(string(raw))
	}
	shell("printf '#!/bin/sh\nexec /tmp/runtime-fixture native\n' > /tmp/native-fixture; chmod 755 /tmp/native-fixture")
	supervised := start("supervised", "one", "/tmp/native-fixture")
	send(supervised, agentwire.Frame{Kind: "sync"})
	runnerWait(t, supervised, func(f agentwire.Frame) bool { return f.Kind == "synced" })
	send(supervised, agentwire.Frame{Kind: "command", CommandID: "descendants", TurnID: "descendant-turn", Payload: json.RawMessage(`{"type":"prompt","message":"spawn"}`)})
	runnerWait(t, supervised, func(f agentwire.Frame) bool { return f.State == "command.completed" })
	got := shell(`for name in group-pid escaped-pid; do p=$(cat /runtime/sessions/supervised/$name); if [ -e /proc/$p/stat ]; then awk '{print $3}' /proc/$p/stat; else echo gone; fi; done`)
	if strings.Count(got, "gone")+strings.Count(got, "Z") != 2 {
		t.Fatal("runner leaked descendants", got)
	}
	t.Log("Session replacement loaded its retained native path without Project pause; runner subreaper settled ordinary and setsid descendants while sibling continued")
	t.Run("application-admission-ingest-projection", func(t *testing.T) {
		agents := NewAgents(runctx, s, l, backend, profile.Bindings(), 8)
		agents.RunnerBinary = "/tmp/agent-runner"
		defer agents.Close()
		for _, id := range []string{"c", "d"} {
			_, err := s.CreateSession(runctx, scope, id, "Agent")
			must(t, err)
			shell("mkdir -p /runtime/sessions/" + id)
			must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/runtime/sessions/"+id+"/models.json", strings.NewReader(models)))
			must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/runtime/sessions/"+id+"/settings.json", strings.NewReader(`{"defaultProvider":"local","defaultModel":"local-model","compaction":{"enabled":false}}`)))
			_, err = s.AdmitTurn(runctx, scope, id, "first", "hello-"+id, false)
			must(t, err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			complete := 0
			for _, id := range []string{"c", "d"} {
				err := agents.Step(runctx, scope, id)
				if err != nil && !errors.Is(err, ErrPending) && !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				turns, err := s.Turns(runctx, scope, id, 0, 100)
				must(t, err)
				if turns[0].State == "completed" {
					complete++
				}
			}
			if complete == 2 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, id := range []string{"c", "d"} {
			turns, err := s.Turns(runctx, scope, id, 0, 100)
			must(t, err)
			if turns[0].State != "completed" {
				t.Fatalf("turn did not settle: %+v", turns)
			}
			messages, err := s.Messages(runctx, scope, id, 0, 100)
			must(t, err)
			if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "deterministic-local-reply" {
				t.Fatalf("messages: %+v", messages)
			}
		}
		t.Log("Project admission -> durable host command -> Go runner -> real Pi -> committed raw ingest/messages/capture -> journal ACK")
	})
	t.Run("native-abort-with-full-normal-journal", func(t *testing.T) {
		started := time.Now()
		evidence := map[string]any{}
		var frames []map[string]any
		t.Cleanup(func() {
			evidence["frames"] = frames
			evidence["elapsed_ms"] = time.Since(started).Milliseconds()
			raw, _ := json.MarshalIndent(evidence, "", "  ")
			_ = os.WriteFile(filepath.Join(os.Getenv("PROJECT_TEST_OUTPUT"), "native-pressure-observation.json"), raw, 0600)
		})
		observe := func(f agentwire.Frame) {
			var native struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(f.Payload, &native)
			if len(frames) < 2048 {
				frames = append(frames, map[string]any{"at_ms": time.Since(started).Milliseconds(), "kind": f.Kind, "state": f.State, "sequence": f.Sequence, "command": f.CommandID, "native_type": native.Type, "payload_bytes": len(f.Payload), "pressure": f.Pressure})
			}
			if f.Kind == "error" {
				t.Fatalf("runner error under pressure: %s", f.Error)
			}
		}
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/runtime/sessions/pressure/models.json", strings.NewReader(models)))
		must(t, api.WriteFile(runctx, r.Reference.SandboxID, "/runtime/sessions/pressure/settings.json", strings.NewReader(`{"defaultProvider":"local","defaultModel":"local-model","compaction":{"enabled":false}}`)))
		o := start("pressure", "pressure-run", "/usr/local/bin/pi", 2<<20)
		defer o.stop()
		send := func(frame agentwire.Frame) {
			t.Helper()
			frame.Session = "pressure"
			frame.Incarnation = "pressure-run"
			frame.Owner = 1
			var raw bytes.Buffer
			must(t, agentwire.Encode(&raw, frame))
			must(t, backend.SendProcessInput(runctx, o.ref, raw.Bytes()))
		}
		send(agentwire.Frame{Kind: "sync"})
		send(agentwire.Frame{Kind: "command", CommandID: "pressure-prompt", TurnID: "pressure-turn", Payload: json.RawMessage(`{"type":"prompt","message":"pressure-native"}`)})
		// Deliberately withhold ACK while still draining transport. Every later
		// ACK follows a synced fixture transcript, never merely a delivered byte.
		capture, err := os.CreateTemp(t.TempDir(), "committed-*.jsonl")
		must(t, err)
		defer capture.Close()
		encoder := json.NewEncoder(capture)
		var last uint64
		var pressure agentwire.Pressure
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		for pressure.FullAttempts == 0 {
			select {
			case f := <-o.frames:
				observe(f)
				if f.Kind == "record" {
					if f.Sequence != last+1 {
						t.Fatal("sequence gap", last, f.Sequence)
					}
					must(t, encoder.Encode(f))
					last = f.Sequence
				}
				if f.Kind == "pressure" && f.Pressure != nil {
					pressure = *f.Pressure
				}
			case err := <-o.errors:
				t.Fatal(err)
			case <-deadline.C:
				t.Fatal("normal journal never reached pressure")
			}
		}
		if pressure.JournalBytes > pressure.JournalLimit-pressure.ControlReserve || pressure.WindowPeak > 1<<20 {
			t.Fatal("normal traffic consumed reserve", pressure)
		}
		evidence["pressure_at_ms"] = time.Since(started).Milliseconds()
		began := time.Now()
		send(agentwire.Frame{Kind: "command", CommandID: "pressure-abort", TurnID: "pressure-turn", Payload: json.RawMessage(`{"type":"abort","run_id":"pressure-prompt"}`)})
		dispatched := false
		for !dispatched {
			select {
			case f := <-o.frames:
				observe(f)
				// Control replies can interleave with already-sent records.
				// Persist those records too; discarding them loses ACK coverage.
				if f.Kind == "record" {
					if f.Sequence != last+1 {
						t.Fatal("sequence gap during Stop dispatch", last, f.Sequence)
					}
					must(t, encoder.Encode(f))
					last = f.Sequence
				}
				if f.Kind == "command_status" && f.CommandID == "pressure-abort" && f.State == "dispatching" {
					dispatched = true
				}
			case err := <-o.errors:
				t.Fatal(err)
			case <-deadline.C:
				t.Fatal("full normal journal blocked Stop")
			}
		}
		latency := time.Since(began)
		evidence["dispatch_ms"] = latency.Milliseconds()
		evidence["first_ack_sequence"] = last
		must(t, capture.Sync())
		send(agentwire.Frame{Kind: "ack", Sequence: last})
		completed := false
		for !completed {
			select {
			case f := <-o.frames:
				observe(f)
				if f.Kind != "record" {
					continue
				}
				if f.Sequence != last+1 {
					t.Fatal("post-Stop sequence gap", last, f.Sequence)
				}
				must(t, encoder.Encode(f))
				must(t, capture.Sync())
				last = f.Sequence
				send(agentwire.Frame{Kind: "ack", Sequence: last})
				if f.CommandID == "pressure-prompt" && (f.State == "command.unknown" || f.State == "command.rejected") {
					t.Fatalf("native prompt settled as %s under pressure", f.State)
				}
				if f.CommandID == "pressure-prompt" && f.State == "command.completed" {
					var result struct{ Outcome string }
					must(t, json.Unmarshal(f.Payload, &result))
					if result.Outcome != "canceled" {
						t.Fatal("native outcome wasn't interruption", result.Outcome)
					}
					completed = true
				}
			case err := <-o.errors:
				t.Fatal(err)
			case <-deadline.C:
				t.Fatal("native interruption didn't settle")
			}
		}
		t.Logf("real native Stop under pressure: dispatch=%s journal=%d/%d reserved=%d window_peak=%d last_committed=%d", latency, pressure.JournalBytes, pressure.JournalLimit, pressure.ControlReserve, pressure.WindowPeak, last)
		must(t, backend.SignalProcess(runctx, o.ref, 15))
	})
}
