package product

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

// Terminal output bounds. The engine keeps a 64 KiB ring per process; the
// application keeps a larger one so an ordinary reconnect repaints a screen
// without an engine replay, and marks a gap when it cannot. There is deliberately
// no durable PTY journal: full terminal history across application restart or
// placement loss is not promised, and pretending otherwise would mean writing
// every keystroke's echo to disk.
const (
	terminalReplayBytes   = 256 << 10
	terminalObserverQueue = 16
	terminalInputBytes    = 1 << 20
)

// TerminalFrame is what one observer receives. Data carries raw PTY bytes, which
// the application never interprets. Gap says bytes are missing and the renderer
// must reset rather than draw a corrupted screen.
type TerminalFrame struct {
	Kind     string `json:"kind"`
	Data     []byte `json:"-"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Rows     int    `json:"rows,omitempty"`
	Cols     int    `json:"cols,omitempty"`
	// Input reports whether the receiving observer currently holds input.
	Input bool `json:"input,omitempty"`
}

// TerminalObserver is one browser attachment. Its queue is bounded: a slow or
// stalled observer is disconnected with a gap rather than allowed to stall the
// engine reader, a sibling observer, or Agent ingest.
type TerminalObserver struct {
	frames chan TerminalFrame
	// send guards the frame channel so closing it can never race a delivery. It is
	// held only for a nonblocking send, never across engine or database work.
	send   sync.Mutex
	closed bool
	once   sync.Once
	// input is true while this observer owns the terminal's input.
	input atomic.Bool
	pty   *terminalAttachment
	drops atomic.Uint64
}

// Frames is the observer's output stream. It is closed when the observer is
// detached, the process ends, or authorization is lost, so a consumer can range
// over it and treat the end of the range as the end of the attachment.
func (o *TerminalObserver) Frames() <-chan TerminalFrame { return o.frames }

// HoldsInput reports whether this observer may write. A read-only observer is a
// normal, supported state, not a failure.
func (o *TerminalObserver) HoldsInput() bool { return o.input.Load() }

// Close detaches this observer only. It never signals the PTY: closing a view is
// not closing a terminal.
func (o *TerminalObserver) Close() {
	o.once.Do(func() {
		if o.pty != nil {
			o.pty.detach(o)
		}
		o.send.Lock()
		o.closed = true
		close(o.frames)
		o.send.Unlock()
	})
}

// Drops counts how many times this observer fell behind. It is diagnostic: the
// gap marker, not this counter, is what tells a renderer to reset.
func (o *TerminalObserver) Drops() uint64 { return o.drops.Load() }

func (o *TerminalObserver) deliver(f TerminalFrame) {
	o.send.Lock()
	if o.closed {
		o.send.Unlock()
		return
	}
	select {
	case o.frames <- f:
		o.send.Unlock()
		return
	default:
	}
	// The observer cannot keep up. Record one gap and disconnect it; it resyncs
	// with a fresh snapshot rather than receiving a silently truncated stream. The
	// gap is delivered by displacing the oldest queued frame, because a full queue
	// is exactly when the renderer most needs to be told it missed output.
	o.drops.Add(1)
	select {
	case <-o.frames:
	default:
	}
	select {
	case o.frames <- TerminalFrame{Kind: "gap", Reason: "observer too slow"}:
	default:
	}
	o.send.Unlock()
	// Close takes the same lock, so it must run after the unlock above.
	go o.Close()
}

type terminalAttachment struct {
	projectID   string
	incarnation TerminalIncarnation
	cancel      context.CancelFunc

	mu        sync.Mutex
	observers map[*TerminalObserver]struct{}
	owner     *TerminalObserver
	replay    []byte
	truncated bool
	ready     bool
	err       error

	// input serializes writes to the engine's ordered input stream. The engine
	// guarantees ordering within one stream; this guarantees one writer holds it.
	input sync.Mutex
}

// TerminalBackend is the process substrate a Terminal needs. It is the ordinary
// process port plus PTY allocation and resize.
type TerminalBackend interface {
	InspectProcess(context.Context, sandbox.ProcessReference) (sandbox.ProcessObservation, error)
	sandbox.Processes
	StartPTY(context.Context, sandbox.StartProcessRequest, int, int) (sandbox.ProcessStream, error)
	ResizePTY(context.Context, sandbox.ProcessReference, int, int) error
	// SendPTYInput writes to the PTY master. It is distinct from process stdin:
	// they are separate engine input branches, and keystrokes belong on this one.
	SendPTYInput(context.Context, sandbox.ProcessReference, []byte) error
}

// Terminals owns one engine attachment per live PTY and fans its output out to
// authorized browsers. Browsers never reach the engine, and a browser's lifetime
// never determines the process's.
type Terminals struct {
	Store     *Store
	Lifecycle *Lifecycle
	Backend   TerminalBackend
	Profile   Profile
	// Shell and its arguments are deployment configuration, not user input. A
	// Terminal has no user-supplied command: that would be a way to ask the
	// application to run something the Project policy never declared.
	Shell         string
	ShellArgs     []string
	Directory     string
	Environment   []string
	MaxTerminals  int
	MaxPerProject int
	// TerminationGrace bounds how long an explicit close waits for a polite exit
	// before escalating to SIGKILL. Zero uses a small default.
	TerminationGrace time.Duration

	mu          sync.Mutex
	attachments map[string]*terminalAttachment
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewTerminals(ctx context.Context, s *Store, l *Lifecycle, b TerminalBackend, profile Profile, max int) *Terminals {
	ctx, cancel := context.WithCancel(ctx)
	return &Terminals{
		Store: s, Lifecycle: l, Backend: b, Profile: profile,
		Shell: "/bin/bash", ShellArgs: []string{"-l"}, Directory: "/workspace",
		Environment:  []string{"HOME=/root", "TERM=xterm-256color", "PATH=/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin"},
		MaxTerminals: max, attachments: map[string]*terminalAttachment{}, ctx: ctx, cancel: cancel,
	}
}

// Close detaches every attachment. It does not stop any PTY: an application
// shutdown is not evidence that a guest process ended, and the next owner adopts
// the same handles.
func (t *Terminals) Close() {
	t.cancel()
	t.mu.Lock()
	for _, a := range t.attachments {
		a.cancel()
	}
	t.mu.Unlock()
	t.wg.Wait()
}

// Start ensures the Terminal has a live PTY and returns its incarnation. It is
// idempotent: a Terminal that is already running returns its current process
// rather than starting a second one. It reports ErrPending while the Project
// placement or the process start is still converging.
func (t *Terminals) Start(ctx context.Context, scope Scope, terminal string, rows, cols int) (TerminalIncarnation, error) {
	child, err := t.Store.Terminal(ctx, scope, terminal)
	if err != nil {
		return TerminalIncarnation{}, err
	}
	// Deletion closes admission here, not only in reserveTerminal: a Terminal that
	// already has an attachment would otherwise keep answering Start after the user
	// asked for it to go away.
	if child.Deletion != Live {
		return TerminalIncarnation{}, ErrDeleting
	}
	if err = t.Lifecycle.EnsureConfiguredActive(ctx, scope, t.Profile); err != nil {
		return TerminalIncarnation{}, err
	}
	if _, err = t.Lifecycle.ActiveResources(ctx, scope); err != nil {
		return TerminalIncarnation{}, err
	}
	a, err := t.ensure(ctx, scope, terminal, rows, cols)
	if err != nil {
		return TerminalIncarnation{}, err
	}
	a.mu.Lock()
	ready, attachErr, i := a.ready, a.err, a.incarnation
	a.mu.Unlock()
	if attachErr != nil {
		return i, attachErr
	}
	if !ready {
		return i, ErrPending
	}
	return i, nil
}

func (t *Terminals) ensure(ctx context.Context, scope Scope, terminal string, rows, cols int) (*terminalAttachment, error) {
	t.mu.Lock()
	a := t.attachments[terminal]
	var existingError error
	if a != nil {
		a.mu.Lock()
		existingError = a.err
		a.mu.Unlock()
	}
	if a != nil && existingError != nil {
		// A failed attachment is discarded and its reason reported, rather than
		// replaced silently. Returning ErrPending here would make a PTY that cannot
		// start indistinguishable from one that is still starting, forever.
		failure := existingError
		a.cancel()
		delete(t.attachments, terminal)
		t.mu.Unlock()
		return nil, failure
	}
	if a != nil {
		t.mu.Unlock()
		return a, nil
	}
	projectCount := 0
	for _, current := range t.attachments {
		if current.projectID == scope.ProjectID {
			projectCount++
		}
	}
	perProject := t.MaxPerProject
	if perProject < 1 {
		perProject = min(t.MaxTerminals, max(2, t.MaxTerminals/2))
	}
	if t.MaxTerminals < 1 || len(t.attachments) >= t.MaxTerminals || projectCount >= perProject {
		t.mu.Unlock()
		return nil, ErrCapacity
	}
	attachctx, cancel := context.WithCancel(t.ctx)
	a = &terminalAttachment{cancel: cancel, projectID: scope.ProjectID, observers: map[*TerminalObserver]struct{}{}}
	t.attachments[terminal] = a
	t.mu.Unlock()
	abandon := func(err error) (*terminalAttachment, error) {
		t.mu.Lock()
		delete(t.attachments, terminal)
		t.mu.Unlock()
		cancel()
		return nil, err
	}
	i, err := t.Store.reserveTerminal(ctx, scope, terminal, rows, cols)
	if err != nil {
		return abandon(err)
	}
	// Fence any previous application owner before touching the process, so its
	// input and exit writes are rejected from here on.
	i, err = t.Store.takeTerminalOwnership(ctx, i)
	if err != nil {
		return abandon(err)
	}
	a.mu.Lock()
	a.incarnation = i
	a.mu.Unlock()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		err := t.attach(attachctx, a)
		a.mu.Lock()
		a.err = err
		a.ready = false
		observers := make([]*TerminalObserver, 0, len(a.observers))
		for o := range a.observers {
			observers = append(observers, o)
		}
		a.mu.Unlock()
		for _, o := range observers {
			o.Close()
		}
	}()
	return a, nil
}

// Attach subscribes one browser to a live terminal. input requests the single
// input claim; when another observer already holds it, the attachment succeeds
// read-only rather than stealing the keyboard.
func (t *Terminals) Attach(ctx context.Context, scope Scope, terminal string, input bool) (*TerminalObserver, error) {
	child, err := t.Store.Terminal(ctx, scope, terminal)
	if err != nil {
		return nil, err
	}
	if child.Deletion != Live {
		return nil, ErrDeleting
	}
	t.mu.Lock()
	a := t.attachments[terminal]
	t.mu.Unlock()
	if a == nil {
		return nil, ErrNotFound
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	if len(a.observers) >= 8 {
		return nil, ErrCapacity
	}
	o := &TerminalObserver{frames: make(chan TerminalFrame, terminalObserverQueue), pty: a}
	if input && a.owner == nil {
		a.owner = o
		o.input.Store(true)
	}
	a.observers[o] = struct{}{}
	// A joining observer gets the retained screen bytes, preceded by a gap when the
	// ring already dropped older output, then live frames. The replay is one frame
	// so it cannot itself overflow a fresh queue.
	if a.truncated {
		o.deliver(TerminalFrame{Kind: "gap", Reason: "output older than the retained buffer"})
	}
	if len(a.replay) > 0 {
		o.deliver(TerminalFrame{Kind: "data", Data: append([]byte(nil), a.replay...)})
	}
	o.deliver(TerminalFrame{Kind: "status", Rows: a.incarnation.Rows, Cols: a.incarnation.Cols, Input: o.input.Load()})
	return o, nil
}

func (a *terminalAttachment) detach(o *TerminalObserver) {
	a.mu.Lock()
	delete(a.observers, o)
	released := a.owner == o
	if released {
		a.owner = nil
	}
	var remaining []*TerminalObserver
	if released {
		for other := range a.observers {
			remaining = append(remaining, other)
		}
	}
	rows, cols := a.incarnation.Rows, a.incarnation.Cols
	a.mu.Unlock()
	o.input.Store(false)
	// Input is not silently promoted to another view. The remaining observers are
	// told it is free so a user can take it deliberately.
	for _, other := range remaining {
		other.deliver(TerminalFrame{Kind: "status", Rows: rows, Cols: cols})
	}
}

// ClaimInput gives one observer the input claim when no live observer holds it.
// There is no takeover: two views must not drive one keyboard, and stealing it
// silently would make lost keystrokes look like a terminal fault.
func (t *Terminals) ClaimInput(o *TerminalObserver) error {
	if o == nil || o.pty == nil {
		return ErrInvalid
	}
	a := o.pty
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, attached := a.observers[o]; !attached {
		return ErrNotFound
	}
	if a.owner != nil && a.owner != o {
		return ErrConflict
	}
	a.owner = o
	o.input.Store(true)
	o.deliver(TerminalFrame{Kind: "status", Rows: a.incarnation.Rows, Cols: a.incarnation.Cols, Input: true})
	return nil
}

// Input writes raw bytes to the PTY on behalf of the observer that holds the
// claim. Unacknowledged input is never replayed after a reconnect: repeating
// keystrokes into a live shell is a side effect, not a recovery.
func (t *Terminals) Input(ctx context.Context, o *TerminalObserver, data []byte) error {
	if o == nil || o.pty == nil {
		return ErrInvalid
	}
	if len(data) == 0 {
		return nil
	}
	if len(data) > terminalInputBytes {
		return ErrInvalid
	}
	if !o.input.Load() {
		return ErrConflict
	}
	a := o.pty
	a.mu.Lock()
	ready, err, i := a.ready, a.err, a.incarnation
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if !ready {
		return ErrPending
	}
	a.input.Lock()
	defer a.input.Unlock()
	if !o.input.Load() {
		return ErrConflict
	}
	if err = t.Store.terminalAuthority(ctx, i); err != nil {
		return err
	}
	return t.Backend.SendPTYInput(ctx, i.Reference, data)
}

// Resize converges to the latest dimensions and tells a live PTY. It is allowed
// from any observer: the window size is a property of the terminal, and a
// read-only viewer resizing its own pane is not an input side effect.
func (t *Terminals) Resize(ctx context.Context, scope Scope, terminal string, rows, cols int) error {
	i, err := t.Store.ResizeTerminal(ctx, scope, terminal, rows, cols)
	if err != nil {
		return err
	}
	if i.ID == "" {
		// No live process: the persisted size is the whole effect.
		return nil
	}
	if err = t.Backend.ResizePTY(ctx, i.Reference, rows, cols); err != nil {
		return err
	}
	t.mu.Lock()
	a := t.attachments[terminal]
	t.mu.Unlock()
	if a == nil {
		return nil
	}
	a.mu.Lock()
	a.incarnation.Rows, a.incarnation.Cols = rows, cols
	observers := make([]*TerminalObserver, 0, len(a.observers))
	for o := range a.observers {
		observers = append(observers, o)
	}
	a.mu.Unlock()
	for _, o := range observers {
		o.deliver(TerminalFrame{Kind: "status", Rows: rows, Cols: cols, Input: o.input.Load()})
	}
	return nil
}

// Interrupt is a terminal's Ctrl-C. It writes the interrupt character to the PTY
// so the tty line discipline delivers SIGINT to the foreground process group,
// which is what a real terminal does.
//
// A directed signal is deliberately not used here. The engine signals the whole
// process group of the handle it was given, and the interactive shell is in that
// group, so a directed SIGINT ends the shell itself rather than the command the
// user wanted to interrupt. Measured on the pinned engine; see the S05 evidence.
func (t *Terminals) Interrupt(ctx context.Context, o *TerminalObserver) error {
	return t.Input(ctx, o, []byte{0x03})
}

// Signal delivers a directed signal to the PTY's process group. It is the
// termination path, not the interrupt path: use Interrupt for Ctrl-C.
func (t *Terminals) Signal(ctx context.Context, scope Scope, terminal string, signal int32) error {
	i, err := t.Store.LiveTerminal(ctx, scope, terminal)
	if err != nil {
		return err
	}
	return t.Backend.SignalProcess(ctx, i.Reference, signal)
}

// EOF closes the PTY's input, which a shell sees as Ctrl-D. It is a request: the
// shell decides whether to exit, and only its exit frame settles the incarnation.
func (t *Terminals) EOF(ctx context.Context, scope Scope, terminal string) error {
	i, err := t.Store.LiveTerminal(ctx, scope, terminal)
	if err != nil {
		return err
	}
	return t.Backend.CloseProcessInput(ctx, i.Reference)
}

// Stop requests termination of a Terminal's PTY. It is what an explicit close and
// child cleanup use. The attachment is deliberately left in place so its own
// stream observes the terminal exit frame: that frame is the only evidence that
// records the exit code, and a cancelled transport is not an exit.
func (t *Terminals) Stop(ctx context.Context, project, terminal string) error {
	i, err := liveTerminalIncarnation(ctx, t.Store.reader, terminal)
	if errors.Is(err, ErrNotFound) {
		// Nothing is running. That is the settled state Stop asks for.
		return nil
	}
	if err != nil {
		return err
	}
	if i.ProjectID != project {
		return ErrNotFound
	}
	t.mu.Lock()
	if t.attachments[terminal] == nil {
		owned, e := t.Store.takeTerminalOwnership(ctx, i)
		if e != nil {
			t.mu.Unlock()
			return e
		}
		attachctx, cancel := context.WithCancel(t.ctx)
		attachment := &terminalAttachment{projectID: project, incarnation: owned, cancel: cancel, observers: map[*TerminalObserver]struct{}{}}
		t.attachments[terminal] = attachment
		i = owned
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			e := t.attach(attachctx, attachment)
			attachment.mu.Lock()
			attachment.err = e
			attachment.ready = false
			attachment.mu.Unlock()
		}()
	}
	t.mu.Unlock()
	if err = t.signal(ctx, i, 15); err != nil {
		return err
	}
	// An interactive shell ignores SIGTERM: it is a login shell with job control,
	// so a polite request leaves it running forever. Measured on the pinned engine.
	// Wait a bounded grace period for the exit frame the attachment observes, then
	// escalate. Escalating is what makes "close this terminal" actually close it.
	deadline := time.Now().Add(t.terminationGrace())
	for time.Now().Before(deadline) {
		if _, err = liveTerminalIncarnation(ctx, t.Store.reader, terminal); errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return t.signal(ctx, i, 9)
}

// signal requests one signal and treats an absent handle as loss rather than as
// an exit: the process is gone, but its outcome was never observed.
func (t *Terminals) signal(ctx context.Context, i TerminalIncarnation, signal int32) error {
	if err := t.Backend.SignalProcess(ctx, i.Reference, signal); err != nil {
		if sandbox.Is(err, sandbox.Precondition) {
			return ErrPending
		}
		if sandbox.Is(err, sandbox.NotFound) {
			return t.Store.LoseTerminal(ctx, i, "process handle absent at termination")
		}
		return err
	}
	return nil
}

func (t *Terminals) terminationGrace() time.Duration {
	if t.TerminationGrace > 0 {
		return t.TerminationGrace
	}
	return 3 * time.Second
}

func (t *Terminals) attach(ctx context.Context, a *terminalAttachment) error {
	i := a.incarnation
	var stream sandbox.ProcessStream
	var err error
	if i.State == "starting" {
		stream, err = t.Backend.StartPTY(ctx, sandbox.StartProcessRequest{
			ProcessReference: i.Reference, IdempotencyKey: i.ID,
			Command: t.Shell, Arguments: t.ShellArgs, Directory: t.Directory, Environment: t.Environment,
		}, i.Rows, i.Cols)
	} else {
		observation, e := t.Backend.InspectProcess(ctx, i.Reference)
		if e != nil {
			return e
		}
		if observation.State == "exited" && observation.ExitCode != nil {
			if e = t.Store.SettleTerminalExit(ctx, i, *observation.ExitCode); e != nil {
				return e
			}
			t.broadcast(a, TerminalFrame{Kind: "exit", ExitCode: observation.ExitCode})
			return nil
		}
		if observation.State == "lost" {
			return t.Store.LoseTerminal(ctx, i, "retained process record reports loss")
		}
		if observation.State != "running" && observation.State != "unknown" {
			return ErrPending
		}
		if observation.State == "running" {
			a.mu.Lock()
			a.ready = true
			a.incarnation.State = "running"
			a.mu.Unlock()
		}
		// An absent live process with stale control-plane state still needs Connect:
		// its retained end frame may be the observation that advances that projection.

		stream, err = t.Backend.ConnectProcess(ctx, i.Reference, 0)
	}
	if err != nil {
		return err
	}
	defer stream.Close()
	// Next blocks until the engine sends a frame, so cancellation has to close the
	// stream rather than merely mark the context done. Closing detaches this
	// reader; it is not a signal to the PTY and does not end the process.
	detached := make(chan struct{})
	defer close(detached)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-detached:
		}
	}()
	for {
		f, err := stream.Next()
		if err != nil {
			if sandbox.Is(err, sandbox.NotFound) && i.State == "running" {
				return t.Store.LoseTerminal(ctx, i, "original PTY handle is no longer retained")
			}
			if errors.Is(err, io.EOF) {
				// The transport ended without a terminal frame. That is a lost stream,
				// not a process exit, so nothing is settled here.
				return io.EOF
			}
			return err
		}
		switch {
		case f.Kind == "start":
			if err = t.Store.TerminalRunning(ctx, i); err != nil {
				return err
			}
			a.mu.Lock()
			a.ready = true
			// The attachment's snapshot advances with the durable record. Without
			// this, Start would keep reporting the reserve-time state and a caller
			// could never observe the PTY running. Identity is untouched: only this
			// incarnation's state changed.
			a.incarnation.State = "running"
			a.mu.Unlock()
			t.broadcast(a, TerminalFrame{Kind: "status", Rows: i.Rows, Cols: i.Cols})
		case f.Kind == "end":
			code := int(f.ExitCode)
			if err = t.Store.SettleTerminalExit(ctx, i, code); err != nil {
				return err
			}
			t.broadcast(a, TerminalFrame{Kind: "exit", ExitCode: &code})
			return nil
		case f.Gap || f.ResetReason != "":
			reason := f.ResetReason
			if reason == "" {
				reason = "engine output gap"
			}
			a.mu.Lock()
			a.replay, a.truncated = nil, true
			a.mu.Unlock()
			t.broadcast(a, TerminalFrame{Kind: "gap", Reason: reason})
		case f.Kind == "data":
			if len(f.Data) == 0 {
				continue
			}
			t.retain(a, f.Data)
			t.broadcast(a, TerminalFrame{Kind: "data", Data: f.Data})
		}
	}
}

func (t *Terminals) retain(a *terminalAttachment, data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.replay = append(a.replay, data...)
	if len(a.replay) > terminalReplayBytes {
		// The retained window is bounded. Dropping the oldest bytes is why a late
		// joiner is told its screen is incomplete instead of being shown a
		// plausible-looking but wrong one.
		a.replay = append([]byte(nil), a.replay[len(a.replay)-terminalReplayBytes:]...)
		a.truncated = true
	}
}

func (t *Terminals) broadcast(a *terminalAttachment, f TerminalFrame) {
	a.mu.Lock()
	observers := make([]*TerminalObserver, 0, len(a.observers))
	for o := range a.observers {
		observers = append(observers, o)
	}
	a.mu.Unlock()
	for _, o := range observers {
		frame := f
		frame.Input = o.input.Load()
		o.deliver(frame)
	}
}

// Reconcile settles the terminals of obsolete placements and drops attachments
// the store no longer recognizes. A bounded worker calls it per Project; it never
// starts a process and never pauses anything.
func (t *Terminals) Reconcile(ctx context.Context, project string) error {
	r, err := t.Store.resourcesFor(ctx, project)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if _, err = t.Store.LoseTerminalsForPlacement(ctx, project, r.Reference.Epoch, "placement replaced; PTY process did not survive"); err != nil {
		return err
	}
	live, err := t.Store.LiveTerminals(ctx, project, 500)
	if err != nil {
		return err
	}
	recognized := map[string]string{}
	for _, i := range live {
		recognized[i.TerminalID] = i.ID
	}
	t.mu.Lock()
	var stale []*terminalAttachment
	for terminal, a := range t.attachments {
		a.mu.Lock()
		if a.incarnation.ProjectID == project && recognized[terminal] != a.incarnation.ID {
			stale = append(stale, a)
			delete(t.attachments, terminal)
		}
		a.mu.Unlock()
	}
	t.mu.Unlock()
	for _, a := range stale {
		a.cancel()
	}
	return nil
}

// Observer controls retain the attachment's original incarnation. A delayed EOF,
// resize or signal can never resolve the stable Terminal ID to a successor PTY.
func (t *Terminals) observerIdentity(ctx context.Context, o *TerminalObserver, input bool) (TerminalIncarnation, error) {
	if o == nil || o.pty == nil {
		return TerminalIncarnation{}, ErrInvalid
	}
	if input && !o.input.Load() {
		return TerminalIncarnation{}, ErrConflict
	}
	o.pty.mu.Lock()
	i := o.pty.incarnation
	_, attached := o.pty.observers[o]
	o.pty.mu.Unlock()
	if !attached {
		return i, ErrConflict
	}
	return i, t.Store.terminalAuthority(ctx, i)
}
func (t *Terminals) ObserverEOF(ctx context.Context, o *TerminalObserver) error {
	i, err := t.observerIdentity(ctx, o, true)
	if err != nil {
		return err
	}
	return t.Backend.CloseProcessInput(ctx, i.Reference)
}
func (t *Terminals) ObserverSignal(ctx context.Context, o *TerminalObserver, signal int32) error {
	if signal != 15 && signal != 9 {
		return ErrInvalid
	}
	i, err := t.observerIdentity(ctx, o, true)
	if err != nil {
		return err
	}
	return t.Backend.SignalProcess(ctx, i.Reference, signal)
}
func (t *Terminals) ObserverResize(ctx context.Context, scope Scope, o *TerminalObserver, rows, cols int) error {
	i, err := t.observerIdentity(ctx, o, false)
	if err != nil {
		return err
	}
	updated, err := t.Store.resizeTerminal(ctx, scope, i.TerminalID, rows, cols, &i)
	if err != nil {
		return err
	}
	if err = t.Backend.ResizePTY(ctx, updated.Reference, rows, cols); err != nil {
		return err
	}
	o.pty.mu.Lock()
	o.pty.incarnation.Rows = rows
	o.pty.incarnation.Cols = cols
	o.pty.mu.Unlock()
	t.broadcast(o.pty, TerminalFrame{Kind: "status", Rows: rows, Cols: cols})
	return nil
}
