package product

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

// fakePTY is a scripted PTY substrate. It records what the application asked the
// engine to do, so the tests can assert on identity and fencing rather than on
// terminal rendering, which is the browser gate's job.
type fakePTY struct {
	mu      sync.Mutex
	streams map[string]*fakePTYStream
	input   map[string][]byte
	sizes   map[string][2]int
	signals map[string][]int32
	eof     map[string]int
	starts  int
	// refuse makes the next signal report the process handle as absent.
	refuse bool
}

type fakePTYStream struct {
	frames chan sandbox.ProcessFrame
	closed chan struct{}
	once   sync.Once
}

func newFakePTY() *fakePTY {
	return &fakePTY{streams: map[string]*fakePTYStream{}, input: map[string][]byte{}, sizes: map[string][2]int{}, signals: map[string][]int32{}, eof: map[string]int{}}
}

func (s *fakePTYStream) Next() (sandbox.ProcessFrame, error) {
	select {
	case f, ok := <-s.frames:
		if !ok {
			return sandbox.ProcessFrame{}, errors.New("stream ended without a terminal frame")
		}
		return f, nil
	case <-s.closed:
		return sandbox.ProcessFrame{}, errors.New("stream closed")
	}
}
func (s *fakePTYStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (f *fakePTY) stream(handle string) *fakePTYStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[handle]
}
func (f *fakePTY) StartPTY(_ context.Context, r sandbox.StartProcessRequest, rows, cols int) (sandbox.ProcessStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Handle == "" || r.Epoch == 0 || r.IdempotencyKey == "" || r.Command == "" {
		return nil, &sandbox.Error{Class: sandbox.Invalid, Operation: "start pty", Cause: errors.New("identity required")}
	}
	f.starts++
	f.sizes[r.Handle] = [2]int{rows, cols}
	s := &fakePTYStream{frames: make(chan sandbox.ProcessFrame, 64), closed: make(chan struct{})}
	f.streams[r.Handle] = s
	return s, nil
}
func (f *fakePTY) StartProcess(context.Context, sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	return nil, errors.New("a Terminal always allocates a PTY")
}
func (f *fakePTY) ConnectProcess(_ context.Context, r sandbox.ProcessReference, _ uint64) (sandbox.ProcessStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.streams[r.Handle]
	if s == nil {
		return nil, &sandbox.Error{Class: sandbox.NotFound, Operation: "connect", Cause: errors.New("no such handle")}
	}
	return s, nil
}
func (f *fakePTY) SendPTYInput(_ context.Context, r sandbox.ProcessReference, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.streams[r.Handle] == nil {
		return &sandbox.Error{Class: sandbox.NotFound, Operation: "send pty input", Cause: errors.New("no such handle")}
	}
	f.input[r.Handle] = append(f.input[r.Handle], data...)
	return nil
}
func (f *fakePTY) SendProcessInput(context.Context, sandbox.ProcessReference, []byte) error {
	return errors.New("terminal input must use the PTY branch")
}
func (f *fakePTY) ResizePTY(_ context.Context, r sandbox.ProcessReference, rows, cols int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.streams[r.Handle] == nil {
		return &sandbox.Error{Class: sandbox.NotFound, Operation: "resize pty", Cause: errors.New("no such handle")}
	}
	f.sizes[r.Handle] = [2]int{rows, cols}
	return nil
}
func (f *fakePTY) SignalProcess(_ context.Context, r sandbox.ProcessReference, signal int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		return &sandbox.Error{Class: sandbox.NotFound, Operation: "signal", Cause: errors.New("handle absent")}
	}
	if f.streams[r.Handle] == nil {
		return &sandbox.Error{Class: sandbox.NotFound, Operation: "signal", Cause: errors.New("no such handle")}
	}
	f.signals[r.Handle] = append(f.signals[r.Handle], signal)
	return nil
}
func (f *fakePTY) CloseProcessInput(_ context.Context, r sandbox.ProcessReference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eof[r.Handle]++
	return nil
}

func terminalFixture(t *testing.T) (*Terminals, *fakePTY, Scope) {
	t.Helper()
	l, scope, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, scope, testProfile()))
	drive(t, l, scope.ProjectID)
	backend := newFakePTY()
	terminals := NewTerminals(ctx, l.Store, l, backend, testProfile(), 8)
	t.Cleanup(terminals.Close)
	return terminals, backend, scope
}

// start brings one Terminal to running and returns its incarnation. Start is
// asynchronous by design: the attachment owns the process, not the HTTP request.
func startTerminal(t *testing.T, terminals *Terminals, backend *fakePTY, scope Scope, id string, rows, cols int) TerminalIncarnation {
	t.Helper()
	_, err := terminals.Store.CreateTerminal(ctx, scope, id, "Terminal", rows, cols)
	must(t, err)
	i, err := terminals.Start(ctx, scope, id, rows, cols)
	if err != nil && !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := backend.stream(i.Reference.Handle); s != nil {
			s.frames <- sandbox.ProcessFrame{Kind: "start", Sequence: 1}
			break
		}
		time.Sleep(time.Millisecond)
	}
	for time.Now().Before(deadline) {
		live, err := terminals.Store.LiveTerminal(ctx, scope, id)
		terminals.mu.Lock()
		attachment := terminals.attachments[id]
		terminals.mu.Unlock()
		ready := false
		if attachment != nil {
			attachment.mu.Lock()
			ready = attachment.ready && attachment.err == nil
			attachment.mu.Unlock()
		}
		if err == nil && live.State == "running" && ready {
			return live
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal did not reach running")
	return TerminalIncarnation{}
}

func awaitFrame(t *testing.T, o *TerminalObserver, match func(TerminalFrame) bool) TerminalFrame {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var seen []TerminalFrame
	for {
		select {
		case f, ok := <-o.Frames():
			if !ok {
				t.Fatalf("observer closed before the expected frame: %+v", seen)
			}
			seen = append(seen, f)
			if match(f) {
				return f
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for frame, saw %+v", seen)
		}
	}
}

func TestTerminalProcessIdentityLifetimeAndExitEvidence(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	s := terminals.Store

	// A created Terminal owns no process and pins nothing.
	_, err := s.CreateTerminal(ctx, scope, "idle-row", "Terminal", 24, 80)
	must(t, err)
	_, err = s.ArmIdle(ctx, scope, time.Second)
	must(t, err)

	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)
	// A live PTY pins the Project even with no observer attached.
	_, err = s.ArmIdle(ctx, scope, time.Second)
	wantError(t, err, ErrBusy)
	bs, err := s.Blockers(ctx, scope)
	must(t, err)
	if len(bs) != 1 || bs[0].Kind != "terminal_process" || bs[0].ID != i.ID {
		t.Fatal(bs)
	}

	// Starting again is idempotent: one Terminal is one process.
	again, err := terminals.Start(ctx, scope, "shell", 24, 80)
	must(t, err)
	if again.ID != i.ID || backend.starts != 1 {
		t.Fatal(again, backend.starts)
	}

	// A stale incarnation cannot record an exit for its successor's owner.
	stale := i
	stale.Owner = i.Owner - 1
	wantError(t, s.SettleTerminalExit(ctx, stale, 0), ErrConflict)

	backend.stream(i.Reference.Handle).frames <- sandbox.ProcessFrame{Kind: "end", ExitCode: 130, Sequence: 2}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		child, err := s.Terminal(ctx, scope, "shell")
		must(t, err)
		if child.State == "exited" {
			if child.ExitCode == nil || *child.ExitCode != 130 {
				t.Fatal("exit code must come from the terminal frame", child)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	// An exited terminal keeps its metadata and pins nothing.
	_, err = s.ArmIdle(ctx, scope, time.Second)
	must(t, err)
	_, err = s.LiveTerminal(ctx, scope, "shell")
	wantError(t, err, ErrNotFound)

	// Reopening is a new incarnation with a new handle, never the old process.
	must(t, terminals.Reconcile(ctx, scope.ProjectID))
	next, err := terminals.Start(ctx, scope, "shell", 24, 80)
	if err != nil && !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if next.ID == i.ID || next.Reference.Handle == i.Reference.Handle {
		t.Fatal("reopen must not reuse the ended incarnation", i, next)
	}
}

func TestTerminalFanOutInputOwnershipAndGaps(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)
	stream := backend.stream(i.Reference.Handle)

	writer, err := terminals.Attach(ctx, scope, "shell", true)
	must(t, err)
	defer writer.Close()
	if !writer.HoldsInput() {
		t.Fatal("first attachment requesting input must hold it")
	}
	// A second view asking for input does not steal the keyboard.
	reader, err := terminals.Attach(ctx, scope, "shell", true)
	must(t, err)
	defer reader.Close()
	if reader.HoldsInput() {
		t.Fatal("input must have exactly one holder")
	}
	wantError(t, terminals.ClaimInput(reader), ErrConflict)
	wantError(t, terminals.Input(ctx, reader, []byte("x")), ErrConflict)

	// Raw bytes reach every observer unchanged, including split UTF-8 and control
	// characters: the application never interprets terminal output.
	stream.frames <- sandbox.ProcessFrame{Kind: "data", Stream: "pty", Data: []byte("\x1b[1mhi\x1b[0m \xe4\xb8"), Sequence: 3}
	stream.frames <- sandbox.ProcessFrame{Kind: "data", Stream: "pty", Data: []byte("\x96\x07"), Sequence: 4}
	for _, o := range []*TerminalObserver{writer, reader} {
		var got []byte
		for len(got) < len("\x1b[1mhi\x1b[0m \xe4\xb8\x96\x07") {
			f := awaitFrame(t, o, func(f TerminalFrame) bool { return f.Kind == "data" })
			got = append(got, f.Data...)
		}
		if string(got) != "\x1b[1mhi\x1b[0m 世\x07" {
			t.Fatalf("bytes were not preserved: %q", got)
		}
	}

	must(t, terminals.Input(ctx, writer, []byte("echo hi\r")))
	must(t, terminals.Input(ctx, writer, []byte{0x03}))
	backend.mu.Lock()
	sent := string(backend.input[i.Reference.Handle])
	backend.mu.Unlock()
	if sent != "echo hi\r\x03" {
		t.Fatalf("input must reach the PTY in order and unmodified: %q", sent)
	}

	// An engine gap is reported, never papered over with plausible bytes.
	stream.frames <- sandbox.ProcessFrame{Kind: "data", Gap: true, ResetReason: "replay ring exceeded", Sequence: 9}
	gap := awaitFrame(t, writer, func(f TerminalFrame) bool { return f.Kind == "gap" })
	if gap.Reason != "replay ring exceeded" {
		t.Fatal(gap)
	}

	// Releasing input does not silently promote another view; it is announced.
	writer.Close()
	awaitFrame(t, reader, func(f TerminalFrame) bool { return f.Kind == "status" && !f.Input })
	must(t, terminals.ClaimInput(reader))
	if !reader.HoldsInput() {
		t.Fatal("an explicit claim must succeed once the owner is gone")
	}

	// A late joiner is told its screen is incomplete rather than shown a
	// plausible-looking partial one.
	late, err := terminals.Attach(ctx, scope, "shell", false)
	must(t, err)
	defer late.Close()
	awaitFrame(t, late, func(f TerminalFrame) bool { return f.Kind == "gap" })
}

// A stalled browser must not hold the engine reader, a sibling attachment, or the
// PTY hostage. It is disconnected with an explicit gap and resyncs; everything
// else keeps working.
func TestTerminalSlowObserverIsDisconnectedNotBlocking(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)
	stream := backend.stream(i.Reference.Handle)

	// This observer never reads. Its queue is bounded, so the flood below must be
	// absorbed by dropping it rather than by blocking the reader.
	slow, err := terminals.Attach(ctx, scope, "shell", false)
	must(t, err)

	for n := 0; n < terminalObserverQueue*4; n++ {
		stream.frames <- sandbox.ProcessFrame{Kind: "data", Stream: "pty", Data: []byte(fmt.Sprintf("line-%d\n", n)), Sequence: uint64(10 + n)}
	}

	// The dropped observer is told its stream is incomplete, and its channel ends
	// so a consumer can distinguish detachment from silence.
	sawGap := false
	for f := range slow.Frames() {
		if f.Kind == "gap" {
			sawGap = true
		}
	}
	if !sawGap {
		t.Fatal("a dropped observer must be told its stream is incomplete")
	}
	if slow.Drops() == 0 {
		t.Fatal("a dropped observer must be recorded as having fallen behind")
	}

	// The engine reader kept draining throughout: the process is still live, still
	// pins the Project, and a fresh attachment sees live output and can type.
	live, err := terminals.Store.LiveTerminal(ctx, scope, "shell")
	must(t, err)
	if live.State != "running" {
		t.Fatal(live)
	}
	_, err = terminals.Store.ArmIdle(ctx, scope, time.Second)
	wantError(t, err, ErrBusy)

	fresh, err := terminals.Attach(ctx, scope, "shell", true)
	must(t, err)
	defer fresh.Close()
	if !fresh.HoldsInput() {
		t.Fatal("a fresh attachment must be able to take input the dropped view never held")
	}
	stream.frames <- sandbox.ProcessFrame{Kind: "data", Stream: "pty", Data: []byte("after-flood"), Sequence: 9000}
	awaitFrame(t, fresh, func(f TerminalFrame) bool { return f.Kind == "data" && strings.Contains(string(f.Data), "after-flood") })
	must(t, terminals.Input(ctx, fresh, []byte("still here\r")))
	backend.mu.Lock()
	sent := string(backend.input[i.Reference.Handle])
	backend.mu.Unlock()
	if sent != "still here\r" {
		t.Fatalf("the live PTY did not receive the fresh observer's input: %q", sent)
	}
}

func TestTerminalResizePersistsAndFencesPlacement(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)

	must(t, terminals.Resize(ctx, scope, "shell", 37, 101))
	backend.mu.Lock()
	size := backend.sizes[i.Reference.Handle]
	backend.mu.Unlock()
	if size != [2]int{37, 101} {
		t.Fatal(size)
	}
	child, err := terminals.Store.Terminal(ctx, scope, "shell")
	must(t, err)
	if child.Rows != 37 || child.Cols != 101 {
		t.Fatal("resize must persist for the next incarnation", child)
	}
	// Resizing is not a product outbox event: dimensions travel on the attachment
	// and a resize storm must not flood durable projections.
	events, err := terminals.Store.Events(ctx, scope, 0, 500)
	must(t, err)
	for _, e := range events {
		if strings.Contains(e.Kind, "resize") {
			t.Fatal("resize must not enter the product outbox", e)
		}
	}
	for _, bad := range [][2]int{{0, 80}, {24, 0}, {501, 80}, {24, 1001}} {
		wantError(t, terminals.Resize(ctx, scope, "shell", bad[0], bad[1]), ErrInvalid)
	}
}

func TestTerminalLostPlacementClaimsNoExitCode(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, scope, testProfile()))
	drive(t, l, scope.ProjectID)
	backend := newFakePTY()
	terminals := NewTerminals(ctx, l.Store, l, backend, testProfile(), 8)
	defer terminals.Close()
	s := l.Store
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)

	// Pause is rejected while a PTY is live, so the Project is never silently
	// paused out from under a running terminal.
	p, err := s.Project(ctx, scope)
	must(t, err)
	wantError(t, l.Pause(ctx, scope, p.Revision), ErrBusy)

	// An engine-side placement replacement is different: the process is gone, and
	// the application must say so without inventing an exit status.
	settled, err := s.LoseTerminalsForPlacement(ctx, scope.ProjectID, i.Reference.Epoch+1, "placement replaced; PTY process did not survive")
	must(t, err)
	if settled != 1 {
		t.Fatal(settled)
	}
	child, err := s.Terminal(ctx, scope, "shell")
	must(t, err)
	if child.State != "lost" || child.ExitCode != nil || child.Failure == "" {
		t.Fatal("a lost PTY must carry a reason and no exit code", child)
	}
	// A lost terminal stops pinning the Project, and pause then succeeds.
	_, err = s.ArmIdle(ctx, scope, time.Second)
	must(t, err)
	if _, err = s.LoseTerminalsForPlacement(ctx, scope.ProjectID, i.Reference.Epoch+1, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("loss requires an explicit reason")
	}
}

func TestTerminalStopRequiresEvidenceAndAbsentHandleIsLost(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	s := terminals.Store
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)

	// Stop asks politely, then escalates. An interactive login shell ignores
	// SIGTERM, so without escalation "close this terminal" would never close it;
	// the pinned engine's measured behavior is recorded in the S05 evidence.
	terminals.TerminationGrace = 150 * time.Millisecond
	must(t, terminals.Stop(ctx, scope.ProjectID, "shell"))
	backend.mu.Lock()
	signals := append([]int32(nil), backend.signals[i.Reference.Handle]...)
	backend.mu.Unlock()
	if len(signals) != 2 || signals[0] != 15 || signals[1] != 9 {
		t.Fatalf("stop must request termination then escalate: %v", signals)
	}
	// Even after escalation nothing is settled here: only the process's own exit
	// frame records the code, which this scripted backend never sends.
	live, err := s.LiveTerminal(ctx, scope, "shell")
	must(t, err)
	if live.State != "running" {
		t.Fatal("a signal is not an exit", live)
	}
	must(t, terminals.EOF(ctx, scope, "shell"))
	backend.mu.Lock()
	eof := backend.eof[i.Reference.Handle]
	backend.mu.Unlock()
	if eof != 1 {
		t.Fatal(eof)
	}

	// A handle the engine no longer holds is recorded as lost, not as exit 0.
	backend.mu.Lock()
	backend.refuse = true
	backend.mu.Unlock()
	must(t, terminals.Stop(ctx, scope.ProjectID, "shell"))
	child, err := s.Terminal(ctx, scope, "shell")
	must(t, err)
	if child.State != "lost" || child.ExitCode != nil {
		t.Fatal("an absent handle must not manufacture an exit code", child)
	}
	// Stopping a terminal that is not running is the state Stop asks for.
	must(t, terminals.Stop(ctx, scope.ProjectID, "shell"))
	// Neither the Project nor a sibling was disturbed.
	p, err := s.Project(ctx, scope)
	must(t, err)
	if p.State != "active" {
		t.Fatal(p)
	}
}

func TestTerminalAuthorizationAndDeletionFences(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	s := terminals.Store
	other := Scope{"bob", "b"}
	_, err := s.CreateProject(ctx, other.Actor, other.ProjectID, "Beta")
	must(t, err)
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)

	// A foreign actor and a foreign Project both see nothing, and neither can
	// attach to, resize, or stop a terminal they do not own.
	for _, foreign := range []Scope{other, {"bob", scope.ProjectID}, {scope.Actor, other.ProjectID}, {"", scope.ProjectID}} {
		_, err = terminals.Attach(ctx, foreign, "shell", true)
		wantError(t, err, ErrNotFound)
		wantError(t, terminals.Resize(ctx, foreign, "shell", 30, 90), ErrNotFound)
		wantError(t, terminals.Signal(ctx, foreign, "shell", 2), ErrNotFound)
		_, err = terminals.Start(ctx, foreign, "shell", 24, 80)
		wantError(t, err, ErrNotFound)
	}
	// Stop is a worker operation and takes no actor, so it is fenced on the
	// Project identity it was given instead.
	wantError(t, terminals.Stop(ctx, other.ProjectID, "shell"), ErrNotFound)

	child, err := s.Terminal(ctx, scope, "shell")
	must(t, err)
	must(t, s.DeleteTerminal(ctx, scope, "shell", child.Revision))
	// Deletion closes admission but does not end the process.
	_, err = terminals.Attach(ctx, scope, "shell", true)
	wantError(t, err, ErrDeleting)
	_, err = terminals.Start(ctx, scope, "shell", 24, 80)
	wantError(t, err, ErrDeleting)
	wantError(t, terminals.Resize(ctx, scope, "shell", 30, 90), ErrDeleting)
	pending, err := s.PendingChildren(ctx, 10)
	must(t, err)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	wantError(t, s.SettleChild(ctx, pending[0]), ErrBusy)
	must(t, s.SettleTerminalExit(ctx, i, 0))
	must(t, s.SettleChild(ctx, pending[0]))
}

func TestTerminalReconcilePreservesOtherProjects(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	terminals.Lifecycle.MaxProjects = 3
	other := Scope{Actor: scope.Actor, ProjectID: "other"}
	_, err := terminals.Store.CreateProject(ctx, other.Actor, other.ProjectID, "Other")
	must(t, err)
	must(t, terminals.Lifecycle.EnsureActive(ctx, other, testProfile()))
	drive(t, terminals.Lifecycle, other.ProjectID)
	first := startTerminal(t, terminals, backend, scope, "first-project-pty", 24, 80)
	second := startTerminal(t, terminals, backend, other, "other-project-pty", 24, 80)
	observer, err := terminals.Attach(ctx, other, "other-project-pty", true)
	must(t, err)
	defer observer.Close()
	must(t, terminals.Reconcile(ctx, scope.ProjectID))
	backend.stream(second.Reference.Handle).frames <- sandbox.ProcessFrame{Kind: "data", Stream: "stdout", Data: []byte("sibling Project stays attached")}
	frame := awaitFrame(t, observer, func(frame TerminalFrame) bool { return frame.Kind == "data" })
	if string(frame.Data) != "sibling Project stays attached" {
		t.Fatal(frame)
	}
	must(t, terminals.Input(ctx, observer, []byte("still active")))
	if first.ProjectID == second.ProjectID {
		t.Fatal("test needs distinct Projects")
	}
}

func TestObserverControlsCannotReachSuccessorOrBypassReadOnly(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	i := startTerminal(t, terminals, backend, scope, "controlled", 24, 80)
	owner, err := terminals.Attach(ctx, scope, "controlled", true)
	must(t, err)
	defer owner.Close()
	reader, err := terminals.Attach(ctx, scope, "controlled", false)
	must(t, err)
	defer reader.Close()
	wantError(t, terminals.ObserverEOF(ctx, reader), ErrConflict)
	wantError(t, terminals.ObserverSignal(ctx, reader, 9), ErrConflict)
	_, err = terminals.Store.takeTerminalOwnership(ctx, i)
	must(t, err)
	if err = terminals.Input(ctx, owner, []byte("stale input")); err == nil {
		t.Fatal("stale owner input accepted")
	}
	if err = terminals.ObserverEOF(ctx, owner); err == nil {
		t.Fatal("stale EOF accepted")
	}
	if err = terminals.ObserverResize(ctx, scope, owner, 50, 150); err == nil {
		t.Fatal("stale resize accepted")
	}
	if err = terminals.ObserverSignal(ctx, owner, 9); err == nil {
		t.Fatal("stale signal accepted")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.input[i.Reference.Handle]) > 0 || backend.eof[i.Reference.Handle] > 0 || len(backend.signals[i.Reference.Handle]) > 0 {
		t.Fatal("stale/read-only observer reached process controls")
	}
}

func (f *fakePTY) InspectProcess(_ context.Context, ref sandbox.ProcessReference) (sandbox.ProcessObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.streams[ref.Handle] == nil {
		return sandbox.ProcessObservation{}, &sandbox.Error{Class: sandbox.NotFound}
	}
	return sandbox.ProcessObservation{ProcessReference: ref, State: "running"}, nil
}
