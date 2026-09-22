package product

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sandbox"
)

func TestUndeliveredAbortEscalatesOnlyItsGuardianAfterGrace(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	backend := newFakePTY()
	_, err = backend.StartPTY(ctx, sandbox.StartProcessRequest{ProcessReference: i.Reference, IdempotencyKey: i.ID, Command: "runner"}, 24, 80)
	must(t, err)
	sibling := i.Reference
	sibling.Handle = "sibling-terminal"
	_, err = backend.StartPTY(ctx, sandbox.StartProcessRequest{ProcessReference: sibling, IdempotencyKey: "sibling", Command: "bash"}, 24, 80)
	must(t, err)
	a := NewAgents(ctx, s, nil, backend, testProfile(), 8)
	defer a.Close()
	a.attachments[i.SessionID] = &agentAttachment{projectID: scope.ProjectID, incarnation: i, cancel: func() {}}
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.Interrupt(ctx, scope, i.SessionID, "stop", child.ControlRevision))
	must(t, a.ReconcileControls(ctx, scope.ProjectID))
	if len(backend.signals[i.Reference.Handle]) != 0 {
		t.Fatal("normal abort grace was skipped")
	}
	clock := s.now().Add(31 * time.Second)
	s.now = func() time.Time { return clock }
	must(t, a.ReconcileControls(ctx, scope.ProjectID))
	signals := backend.signals[i.Reference.Handle]
	if len(signals) != 1 || signals[0] != 15 || len(backend.signals[sibling.Handle]) != 0 {
		t.Fatal("stop not scoped to original guardian", backend.signals)
	}
	var state string
	must(t, s.reader.QueryRow(`SELECT state FROM turns WHERE id=?`, c.TurnID).Scan(&state))
	if state != "stopping" {
		t.Fatal("signal fabricated a settled outcome", state)
	}
}

type missingSyncBackend struct{ *fakePTY }

func (b missingSyncBackend) SendProcessInput(context.Context, sandbox.ProcessReference, []byte) error {
	return &sandbox.Error{Class: sandbox.Precondition, Operation: "sync", Cause: errors.New("guardian absent")}
}
func (b missingSyncBackend) InspectProcess(_ context.Context, ref sandbox.ProcessReference) (sandbox.ProcessObservation, error) {
	return sandbox.ProcessObservation{ProcessReference: ref, State: "lost"}, nil
}
func TestFailedInitialSyncRecoversMissingGuardianWithoutSuccess(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	backend := missingSyncBackend{newFakePTY()}
	_, err = backend.StartPTY(ctx, sandbox.StartProcessRequest{ProcessReference: i.Reference, IdempotencyKey: i.ID, Command: "runner"}, 24, 80)
	must(t, err)
	a := NewAgents(ctx, s, nil, backend, testProfile(), 8)
	defer a.Close()
	err = a.attach(ctx, &agentAttachment{projectID: scope.ProjectID, incarnation: i})
	if !sandbox.Is(err, sandbox.Precondition) {
		t.Fatal(err)
	}
	var state, recovery string
	must(t, s.reader.QueryRow(`SELECT state,recovery_state FROM agent_incarnations WHERE id=?`, i.ID).Scan(&state, &recovery))
	if state != "exited" || recovery != "pending" {
		t.Fatal(state, recovery)
	}
	must(t, s.reader.QueryRow(`SELECT state FROM turns WHERE id=?`, c.TurnID).Scan(&state))
	if state != "unknown" {
		t.Fatal("missing guardian fabricated completion", state)
	}
}

func TestAgentInputWaitHonorsCancellation(t *testing.T) {
	a := &Agents{}
	attachment := &agentAttachment{}
	attachment.inputOnce.Do(func() { attachment.input = make(chan struct{}, 1) })
	attachment.input <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.send(ctx, attachment, agentwire.Frame{Kind: "sync"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type delayedAckBackend struct {
	*fakePTY
	ackStarted chan uint64
	ackRelease chan struct{}
}

func (b *delayedAckBackend) SendProcessInput(ctx context.Context, ref sandbox.ProcessReference, data []byte) error {
	frame, err := agentwire.NewDecoder(bytes.NewReader(data)).Next()
	if err != nil {
		return err
	}
	if frame.Kind == "sync" {
		var output bytes.Buffer
		err = agentwire.Encode(&output, agentwire.Frame{Kind: "synced", Session: frame.Session, Incarnation: frame.Incarnation, Owner: frame.Owner})
		b.stream(ref.Handle).frames <- sandbox.ProcessFrame{Kind: "data", Data: output.Bytes()}
		return err
	}
	if frame.Kind == "ack" {
		b.ackStarted <- frame.Sequence
		select {
		case <-b.ackRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func TestSlowAcknowledgementDoesNotBlockDurableIngestion(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	backend := &delayedAckBackend{fakePTY: newFakePTY(), ackStarted: make(chan uint64, 10), ackRelease: make(chan struct{})}
	_, err := backend.StartPTY(ctx, sandbox.StartProcessRequest{ProcessReference: i.Reference, IdempotencyKey: i.ID, Command: "runner"}, 24, 80)
	must(t, err)
	a := NewAgents(ctx, s, nil, backend, testProfile(), 8)
	defer a.Close()
	attachment := &agentAttachment{projectID: scope.ProjectID, incarnation: i}
	runctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.attach(runctx, attachment) }()
	defer func() {
		cancel()
		_ = backend.stream(i.Reference.Handle).Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("attachment did not stop")
		}
	}()
	push := func(seq uint64, kind, payload string) {
		var output bytes.Buffer
		must(t, agentwire.Encode(&output, record(i, c, seq, kind, payload)))
		backend.stream(i.Reference.Handle).frames <- sandbox.ProcessFrame{Kind: "data", Data: output.Bytes()}
	}
	readyDeadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		ready := attachment.ready
		a.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("sync missing")
		}
		time.Sleep(time.Millisecond)
	}
	push(1, "command.applied", `{"success":true}`)
	select {
	case seq := <-backend.ackStarted:
		if seq != 1 {
			t.Fatal(seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ack missing")
	}
	for seq := uint64(2); seq <= 40; seq++ {
		push(seq, "native", `{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"stream"}]}}`)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var committed uint64
		must(t, s.reader.QueryRow(`SELECT ingested_sequence FROM agent_incarnations WHERE id=?`, i.ID).Scan(&committed))
		if committed == 40 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow ACK stalled event ingestion", committed)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(backend.ackRelease)
	select {
	case seq := <-backend.ackStarted:
		if seq != 40 {
			t.Fatal("ACK not coalesced to durable high water", seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("latest committed ACK missing")
	}
}
