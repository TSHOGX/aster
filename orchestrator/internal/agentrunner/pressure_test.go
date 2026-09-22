package agentrunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentjournal"
	"harness-platform/orchestrator/internal/agentwire"
)

func pressureRunner(t *testing.T, limit int64) *Runner {
	t.Helper()
	j, err := agentjournal.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"), "s", "i", limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	if err = j.Sync(context.Background(), 1, 0); err != nil {
		t.Fatal(err)
	}
	return &Runner{config: Config{Session: "s", Incarnation: "i"}, journal: j, commands: map[string]agentjournal.Command{}, wake: make(chan struct{}, 1), ackWake: make(chan struct{}, 1), fatal: make(chan error, 1), syncs: make(chan agentwire.Frame, 8), responses: make(chan agentwire.Frame, 32)}
}
func TestFullSendWindowDoesNotDelayControlAndACKResumesRecords(t *testing.T) {
	r := pressureRunner(t, 16<<20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload := json.RawMessage(`{"text":"` + strings.Repeat("x", 600<<10) + `"}`)
	for n := 0; n < 4; n++ {
		if _, err := r.journal.Append(ctx, agentjournal.Record{Kind: "native", Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	input, output := io.Pipe()
	defer input.Close()
	defer output.Close()
	done := make(chan struct{})
	go func() { defer close(done); r.emit(ctx, output) }()
	defer func() { cancel(); input.Close(); output.Close(); <-done }()
	decoder := agentwire.NewDecoder(input)
	r.syncs <- agentwire.Frame{Owner: 1}
	frame, err := decoder.Next()
	if err != nil || frame.Kind != "synced" {
		t.Fatal(frame.Kind, err)
	}
	frame, err = decoder.Next()
	if err != nil || frame.Sequence != 1 {
		t.Fatal(frame.Sequence, err)
	}
	r.response(agentwire.Frame{Kind: "command_status", CommandID: "stop", State: "dispatching"})
	frame, err = decoder.Next()
	if err != nil || frame.Kind != "command_status" {
		t.Fatal("control was behind full record window", frame.Kind, err)
	}
	if err = r.journal.Ack(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	r.notify()
	frame, err = decoder.Next()
	if err != nil || frame.Sequence != 2 {
		t.Fatal("ACK failed to resume", frame.Sequence, err)
	}
	if peak := r.windowPeak.Load(); peak > sendWindowBytes {
		t.Fatal(peak)
	}
}

func TestJournalPressureWaitReleasesCommandMutexAndPreservesSequence(t *testing.T) {
	r := pressureRunner(t, 1<<20)
	r.native = &pressureNative{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "prompt", TurnID: "turn", Payload: json.RawMessage(`{"type":"prompt","message":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"type":"message_update","text":"` + strings.Repeat("x", 64<<10) + `"}`)
	var last uint64
	for {
		seq, err := r.journal.Append(ctx, agentjournal.Record{Kind: "native", Payload: payload})
		if errors.Is(err, agentjournal.ErrFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = seq
	}
	waiting := make(chan error, 1)
	go func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		waiting <- r.appendNative(ctx, agentjournal.Record{Kind: "native", Payload: payload})
	}()
	<-r.wake
	acquired := make(chan struct{})
	go func() { r.mu.Lock(); close(acquired); r.mu.Unlock() }()
	select {
	case <-acquired:
	case <-ctx.Done():
		t.Fatal("pending output blocked Stop mutex")
	}
	if err := r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "stop", TurnID: "turn", Payload: json.RawMessage(`{"type":"abort"}`)}); err != nil {
		t.Fatal("full normal journal blocked native Stop", err)
	}
	if err := r.journal.Ack(ctx, 1, last); err != nil {
		t.Fatal(err)
	}
	r.ackWake <- struct{}{}
	if err := <-waiting; err != nil {
		t.Fatal(err)
	}
	p, err := r.journal.Position(ctx)
	if err != nil || p.Sequence != last+1 {
		t.Fatal(p, err)
	}
}

func TestSustainedJournalStallRecordsUnknownWithoutSkippingSequence(t *testing.T) {
	r := pressureRunner(t, 1<<20)
	r.native = &pressureNative{}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "prompt", TurnID: "turn", Payload: json.RawMessage(`{"type":"prompt","message":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"type":"message_update","text":"` + strings.Repeat("x", 64<<10) + `"}`)
	var last uint64
	for {
		seq, err := r.journal.Append(ctx, agentjournal.Record{CommandID: "prompt", TurnID: "turn", Kind: "native", Payload: payload})
		if errors.Is(err, agentjournal.ErrFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = seq
	}
	r.mu.Lock()
	err := r.appendNative(ctx, agentjournal.Record{CommandID: "prompt", TurnID: "turn", Kind: "native", Payload: payload})
	r.mu.Unlock()
	if !errors.Is(err, agentjournal.ErrFull) {
		t.Fatal(err)
	}
	rows, err := r.journal.Records(ctx, 1, last, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Sequence != last+1 || rows[0].Kind != "command.unknown" {
		t.Fatal("uncommitted frame was ACKable or outcome was fabricated", rows)
	}
	if _, err = r.journal.Result(ctx, "prompt", "completed", json.RawMessage(`{}`)); !errors.Is(err, agentjournal.ErrConflict) {
		t.Fatal("unknown became successful", err)
	}
}

func TestControlResponsesHaveNoDuplicatedContentPayload(t *testing.T) {
	r := pressureRunner(t, 1<<20)
	r.response(agentwire.Frame{Kind: "command_status", State: "completed", Payload: json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`), Error: strings.Repeat("e", 10000)})
	frame := <-r.responses
	if len(frame.Payload) != 0 || len(frame.Error) > 1024 || r.responsePeak.Load() > 2048 {
		t.Fatal("unbounded control response")
	}
	var encoded bytes.Buffer
	if err := agentwire.Encode(&encoded, agentwire.Frame{Kind: frame.Kind, Session: "s", Incarnation: "i", Owner: 1, State: frame.State}); err != nil {
		t.Fatal(err)
	}
}

type pressureNative struct {
	mu sync.Mutex
	bytes.Buffer
}

func (p *pressureNative) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Buffer.Write(data)
}
func (p *pressureNative) Close() error { return nil }

func TestRunner40And80MiBWithDelayedACK(t *testing.T) {
	for _, mib := range []int{40, 80} {
		t.Run(fmt.Sprint(mib), func(t *testing.T) {
			r := pressureRunner(t, 64<<20)
			native := &pressureNative{}
			r.native = native
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if err := r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "prompt", TurnID: "turn", Payload: json.RawMessage(`{"type":"prompt","message":"pressure"}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.journal.Result(ctx, "prompt", "applied", json.RawMessage(`{"success":true}`)); err != nil {
				t.Fatal(err)
			}
			reader, writer := io.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); r.emit(ctx, writer) }()
			defer func() { cancel(); reader.Close(); writer.Close(); <-done }()
			var received atomic.Uint64
			producedDigest, receivedDigest := sha256.New(), sha256.New()
			var produced atomic.Int64
			var frames atomic.Int64
			errs := make(chan error, 4)
			consumerDone := make(chan struct{})
			go func() {
				defer close(consumerDone)
				decoder := agentwire.NewDecoder(reader)
				for {
					f, err := decoder.Next()
					if err != nil {
						if ctx.Err() == nil {
							errs <- err
						}
						return
					}
					if f.Kind == "record" {
						if f.Sequence != received.Load()+1 {
							errs <- errors.New("record sequence skipped")
							return
						}
						if !json.Valid(f.Payload) {
							errs <- errors.New("record payload corrupted")
							return
						}
						if f.State == "native" {
							_, _ = receivedDigest.Write(f.Payload)
						}
						received.Store(f.Sequence)
						frames.Add(1)
					}
				}
			}()
			r.syncs <- agentwire.Frame{Owner: 1}
			ackDone := make(chan struct{})
			go func() {
				defer close(ackDone)
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						seq := received.Load()
						if seq > 0 {
							if err := r.journal.Ack(ctx, 1, seq); err != nil {
								errs <- err
								return
							}
							r.notify()
							select {
							case r.ackWake <- struct{}{}:
							default:
							}
						}
					}
				}
			}()
			defer func() { cancel(); reader.Close(); writer.Close(); <-ackDone; <-consumerDone }()
			start := time.Now()
			var stopLatency time.Duration
			stopped := false
			for n := 0; produced.Load() < int64(mib)<<20; n++ {
				text := fmt.Sprintf("snapshot-%d ", n) + strings.Repeat("battery experiment capacity retention cycle ", min(12000, (n+1)*256))
				payload, _ := json.Marshal(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}}})
				r.mu.Lock()
				err := r.appendNative(ctx, agentjournal.Record{CommandID: "prompt", TurnID: "turn", Kind: "native", Payload: payload})
				r.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				produced.Add(int64(len(payload)))
				_, _ = producedDigest.Write(payload)
				r.notify()
				if !stopped && produced.Load() > int64(mib/2)<<20 {
					began := time.Now()
					if err = r.command(ctx, agentwire.Frame{Owner: 1, CommandID: "stop", TurnID: "turn", Payload: json.RawMessage(`{"type":"abort"}`)}); err != nil {
						t.Fatal(err)
					}
					stopLatency = time.Since(began)
					stopped = true
					if _, err = r.journal.Result(ctx, "stop", "applied", json.RawMessage(`{"success":true}`)); err != nil {
						t.Fatal(err)
					}
					r.notify()
				}
			}
			last, err := r.journal.Result(ctx, "prompt", "completed", json.RawMessage(`{"outcome":"canceled","digest":"synthetic-capture"}`))
			if err != nil {
				t.Fatal(err)
			}
			r.notify()
			for received.Load() < last {
				select {
				case err := <-errs:
					t.Fatal(err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err = r.journal.Ack(ctx, 1, last); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(producedDigest.Sum(nil), receivedDigest.Sum(nil)) {
				t.Fatal("native payload or order changed under pressure")
			}
			p, err := r.journal.Pressure(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if p.PeakBytes > p.Limit || r.windowPeak.Load() > sendWindowBytes || p.Unacked != 0 {
				t.Fatal("budget or ACK invariant", p, r.windowPeak.Load())
			}
			native.mu.Lock()
			hasAbort := strings.Contains(native.String(), `"type":"abort"`)
			native.mu.Unlock()
			if !hasAbort {
				t.Fatal("Stop never reached native stdin")
			}
			result, _ := json.Marshal(map[string]any{"cumulative_target_mib": mib, "raw_bytes": produced.Load(), "records": frames.Load(), "elapsed_ms": time.Since(start).Milliseconds(), "ack_interval_ms": 100, "native_abort_write_ns": stopLatency.Nanoseconds(), "journal": p, "send_window_peak": r.windowPeak.Load(), "control_queue_peak": r.responsePeak.Load()})
			t.Log(string(result))
		})
	}
}
