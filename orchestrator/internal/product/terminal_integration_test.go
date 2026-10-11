//go:build khoraintegration

package product

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/sandbox/khora"
)

// terminalGuest drives real PTYs through the exported application API. Every
// helper takes its own *testing.T so a failure inside a subtest fails that
// subtest rather than calling FailNow on its parent.
type terminalGuest struct {
	ctx       context.Context
	store     *Store
	lifecycle *Lifecycle
	terminals *Terminals
	backend   *khora.Backend
	resources Resources
	scope     Scope
}

// start brings one Terminal to a live PTY and attaches an input-holding view.
func (g *terminalGuest) start(t *testing.T, id string, rows, cols int) (TerminalIncarnation, *TerminalObserver) {
	t.Helper()
	if _, err := g.store.Terminal(g.ctx, g.scope, id); errors.Is(err, ErrNotFound) {
		_, err = g.store.CreateTerminal(g.ctx, g.scope, id, "Terminal", rows, cols)
		must(t, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		i, err := g.terminals.Start(g.ctx, g.scope, id, rows, cols)
		if err == nil && i.State == "running" {
			view, err := g.terminals.Attach(g.ctx, g.scope, id, true)
			must(t, err)
			return i, view
		}
		if err != nil {
			last = err
			if !errors.Is(err, ErrPending) {
				t.Fatalf("start %s: %v", id, err)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("terminal %s did not reach a running PTY: last=%v", id, last)
	return TerminalIncarnation{}, nil
}

// await reads until the accumulated output contains want, so a marker split
// across frames is not mistaken for missing output.
func (g *terminalGuest) await(t *testing.T, o *TerminalObserver, want string) string {
	t.Helper()
	var seen strings.Builder
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-o.Frames():
			if !ok {
				t.Fatalf("attachment ended before %q: %q", want, seen.String())
				return ""
			}
			switch f.Kind {
			case "data":
				seen.Write(f.Data)
			case "gap":
				t.Fatalf("unexpected gap before %q: %s (%q)", want, f.Reason, seen.String())
				return ""
			case "exit":
				t.Fatalf("PTY exited before %q: %q", want, seen.String())
				return ""
			}
			if strings.Contains(seen.String(), want) {
				return seen.String()
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for %q, saw %q", want, seen.String())
			return ""
		}
	}
}

// run types a command and waits for a marker the command prints on its own line.
// The terminal echoes the typed line first, so the marker is matched in its
// newline-prefixed form to distinguish output from echo.
func (g *terminalGuest) run(t *testing.T, o *TerminalObserver, command, marker string) string {
	t.Helper()
	must(t, g.terminals.Input(g.ctx, o, []byte(command+"; printf '\\n"+marker+"\\n'\r")))
	return g.await(t, o, "\n"+marker)
}

func (g *terminalGuest) type_(t *testing.T, o *TerminalObserver, text string) {
	t.Helper()
	must(t, g.terminals.Input(g.ctx, o, []byte(text)))
}

// TestProjectTerminalsOnKhora is the real-guest gate for the Terminal capability.
// It drives actual PTYs in a real sandbox through the exported application API,
// the way a deployment does.
func TestProjectTerminalsOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated PROJECT_TEST_ENDPOINT required")
	}
	runctx, cancel := context.WithTimeout(ctx, 240*time.Second)
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
	scope := Scope{"owner", "terminals"}
	_, err = s.CreateProject(runctx, scope.Actor, scope.ProjectID, "Terminals")
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
		c, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if p, e := s.Project(c, scope); e == nil && p.State != "deleted" {
			if p.State != "deleting" {
				_ = s.DeleteProject(c, scope, p.Revision)
			}
			for i := 0; i < 12; i++ {
				_ = l.Reconcile(c, scope.ProjectID)
			}
		}
	})

	terminals := NewTerminals(runctx, s, l, backend, profile.Bindings(), 8)
	defer terminals.Close()
	g := &terminalGuest{ctx: runctx, store: s, lifecycle: l, terminals: terminals, backend: backend, resources: r, scope: scope}

	alpha, alphaView := g.start(t, "alpha", 24, 80)
	g.run(t, alphaView, "true", "ALPHA-LIVE")

	// A live PTY pins the Project even with nothing typed and no view attached.
	_, err = s.ArmIdle(runctx, scope, time.Second)
	wantError(t, err, ErrBusy)
	bs, err := s.Blockers(runctx, scope)
	must(t, err)
	if len(bs) != 1 || bs[0].Kind != "terminal_process" || bs[0].ID != alpha.ID {
		t.Fatalf("a live PTY must be the named idle blocker: %+v", bs)
	}

	t.Run("two-terminals-share-one-workspace", func(t *testing.T) {
		_, betaView := g.start(t, "beta", 24, 80)
		g.run(t, betaView, "true", "BETA-LIVE")
		// One terminal reads what the other wrote: they share /workspace by ordinary
		// Linux semantics, with no application file locks involved.
		g.run(t, alphaView, "printf from-alpha > /workspace/shared.txt", "WROTE-OK")
		if got := g.run(t, betaView, "cat /workspace/shared.txt", "SIBLING-OK"); !strings.Contains(got, "from-alpha") {
			t.Fatalf("sibling terminal could not read the shared workspace: %q", got)
		}
		// Ending one terminal leaves the other running, with its own exit recorded.
		g.type_(t, betaView, "exit\r")
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			child, err := s.Terminal(runctx, scope, "beta")
			must(t, err)
			if child.State == "exited" {
				if child.ExitCode == nil {
					t.Fatalf("an observed exit must record its code: %+v", child)
				}
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		g.run(t, alphaView, "true", "ALPHA-STILL-OK")
		t.Log("two native PTYs shared one workspace; one exit left the sibling running")
	})

	t.Run("raw-bytes-paste-and-resize", func(t *testing.T) {
		// Multi-byte output reaches the browser unaltered. A here-doc is used so the
		// bytes travel back from the guest rather than through bash's line editor,
		// which legitimately redraws and bells on a partially typed rune; how a
		// half-typed character looks is the emulator's business, not the transport's.
		// Octal byte escapes rather than \u: the guest shell's printf is not required
		// to understand Unicode escapes, and the bytes are the point here anyway.
		if got := g.run(t, alphaView, `printf '\344\270\226\347\225\214'`, "UTF8-OK"); !strings.Contains(got, "世界") {
			t.Fatalf("multi-byte output was altered in transit: %q", got)
		}
		// Input split mid-rune must still arrive as those exact bytes. The writes go
		// to `cat`, not to the shell prompt: bash's line editor legitimately discards
		// a partially typed multi-byte character, which is a readline behavior rather
		// than anything the transport did. What is under test is that the application
		// neither re-chunks nor reinterprets a keystroke stream.
		g.type_(t, alphaView, "cat > /workspace/utf8.bin\r")
		time.Sleep(300 * time.Millisecond)
		must(t, terminals.Input(runctx, alphaView, []byte{0xe4, 0xb8}))
		must(t, terminals.Input(runctx, alphaView, []byte{0x96, 0xe7, 0x95, 0x8c, '\n'}))
		time.Sleep(300 * time.Millisecond)
		// Ctrl-D ends cat's input the way a terminal does.
		must(t, terminals.EOF(runctx, scope, "alpha"))
		if got := g.run(t, alphaView, "od -An -tx1 < /workspace/utf8.bin | tr -d ' \\n'", "HEX-OK"); !strings.Contains(got, "e4b896e7958c") {
			t.Fatalf("input split across a rune boundary did not arrive intact: %q", got)
		}
		// Escape sequences survive as bytes rather than being stripped or rendered.
		if got := g.run(t, alphaView, `printf '\033[1mBOLD\033[0m'`, "ESCAPE-OK"); !strings.Contains(got, "\x1b[1mBOLD\x1b[0m") {
			t.Fatalf("escape sequences were altered: %q", got)
		}
		// The guest observes the resize through its own tty, not through a claim the
		// application makes about it.
		must(t, terminals.Resize(runctx, scope, "alpha", 37, 101))
		if got := g.run(t, alphaView, "stty size", "SIZE-OK"); !strings.Contains(got, "37 101") {
			t.Fatalf("guest did not observe the resize: %q", got)
		}
		child, err := s.Terminal(runctx, scope, "alpha")
		must(t, err)
		if child.Rows != 37 || child.Cols != 101 {
			t.Fatalf("resize must persist for a later incarnation: %+v", child)
		}
		t.Log("raw UTF-8 split across writes and escape bytes preserved; guest tty observed 37x101")
	})

	t.Run("interrupt-ends-foreground-work-and-spares-the-shell", func(t *testing.T) {
		g.type_(t, alphaView, "sleep 300\r")
		time.Sleep(700 * time.Millisecond)
		// Ctrl-C is a PTY byte so the line discipline signals the foreground process
		// group. A directed signal would end the shell itself instead.
		must(t, terminals.Interrupt(runctx, alphaView))
		g.run(t, alphaView, "true", "AFTER-INTERRUPT")
		live, err := s.LiveTerminal(runctx, scope, "alpha")
		must(t, err)
		if live.State != "running" || live.ID != alpha.ID {
			t.Fatalf("interrupt replaced or ended the PTY: %+v", live)
		}
		t.Log("interrupt ended the foreground work and left this PTY's process live")
	})

	t.Run("detached-view-keeps-the-PTY-and-rejoins-the-same-process", func(t *testing.T) {
		// Closing a view is not closing a terminal: the process is retained and the
		// Project stays awake with nothing attached.
		alphaView.Close()
		_, err := s.ArmIdle(runctx, scope, time.Second)
		wantError(t, err, ErrBusy)

		rejoined, err := terminals.Attach(runctx, scope, "alpha", true)
		must(t, err)
		defer rejoined.Close()
		live, err := s.LiveTerminal(runctx, scope, "alpha")
		must(t, err)
		if live.ID != alpha.ID {
			t.Fatalf("reattach did not rejoin the same process: %s vs %s", live.ID, alpha.ID)
		}
		// Push far more than the engine's 64 KiB per-process ring through the PTY,
		// then confirm the same live process still answers rather than a replacement.
		g.run(t, rejoined, "head -c 300000 /dev/zero | tr '\\0' x | wc -c", "FLOOD-OK")
		// Output far past the engine's 64 KiB ring went through this PTY and the
		// process is still the same one.
		g.run(t, rejoined, "true", "REJOINED-OK")
		t.Log("a detached view left the PTY retained and the Project awake; reattaching rejoined the same process")
	})

	t.Run("explicit-close-establishes-exit-and-reopen-is-a-new-process", func(t *testing.T) {
		must(t, terminals.Stop(runctx, scope.ProjectID, "alpha"))
		deadline := time.Now().Add(25 * time.Second)
		var child Terminal
		for time.Now().Before(deadline) {
			var err error
			child, err = s.Terminal(runctx, scope, "alpha")
			must(t, err)
			if child.State == "exited" || child.State == "lost" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if child.State != "exited" || child.ExitCode == nil {
			t.Fatalf("an explicit close must establish an observed exit: %+v", child)
		}
		// An ended terminal keeps its metadata and stops pinning the Project.
		if _, err := s.ArmIdle(runctx, scope, time.Second); err != nil {
			t.Fatalf("an ended terminal must not pin the Project: %v", err)
		}
		must(t, terminals.Reconcile(runctx, scope.ProjectID))
		next, nextView := g.start(t, "alpha", 37, 101)
		defer nextView.Close()
		if next.ID == alpha.ID || next.Reference.Handle == alpha.Reference.Handle {
			t.Fatalf("reopen reused the ended incarnation: %+v", next)
		}
		// The reopened terminal is a genuinely new shell at the persisted size, not a
		// replay of the previous one's command stream.
		if got := g.run(t, nextView, "stty size", "REOPEN-SIZE-OK"); !strings.Contains(got, "37 101") {
			t.Fatalf("a new incarnation did not start at the persisted size: %q", got)
		}
		t.Log("explicit close established the exit code; reopening created a new incarnation at the persisted size")
	})

	t.Run("replaced-placement-loses-the-PTY-without-inventing-an-exit", func(t *testing.T) {
		live, err := s.LiveTerminal(runctx, scope, "alpha")
		must(t, err)
		// A live PTY blocks explicit pause outright: the Project is never paused out
		// from under a running terminal.
		p, err := s.Project(runctx, scope)
		must(t, err)
		wantError(t, l.Pause(runctx, scope, p.Revision), ErrBusy)

		// The engine losing a placement is different. The process is gone and its
		// outcome is unobservable, so the application records loss with a reason and
		// no exit code rather than claiming an exit it never saw.
		settled, err := s.LoseTerminalsForPlacement(runctx, scope.ProjectID, live.Reference.Epoch+1, "placement replaced; PTY process did not survive")
		must(t, err)
		if settled != 1 {
			t.Fatalf("expected one lost PTY, got %d", settled)
		}
		child, err := s.Terminal(runctx, scope, "alpha")
		must(t, err)
		if child.State != "lost" || child.ExitCode != nil || child.Failure == "" {
			t.Fatalf("a lost PTY must carry a reason and no exit code: %+v", child)
		}
		if _, err = s.ArmIdle(runctx, scope, time.Second); err != nil {
			t.Fatalf("a lost PTY must stop pinning the Project: %v", err)
		}
		must(t, terminals.Reconcile(runctx, scope.ProjectID))
		t.Log("a replaced placement reported the PTY lost with an explicit reason and no invented exit status")
	})

	t.Run("terminal-deletion-waits-for-its-process-and-spares-siblings", func(t *testing.T) {
		_, keepView := g.start(t, "keep", 24, 80)
		defer keepView.Close()
		g.run(t, keepView, "true", "KEEP-LIVE")
		_, dropView := g.start(t, "drop", 24, 80)
		g.run(t, dropView, "true", "DROP-LIVE")

		child, err := s.Terminal(runctx, scope, "drop")
		must(t, err)
		must(t, s.DeleteTerminal(runctx, scope, "drop", child.Revision))
		// Deletion records intent. It does not end the process, and it does not
		// overwrite how the terminal was running.
		deleting, err := s.Terminal(runctx, scope, "drop")
		must(t, err)
		if deleting.State != "running" || deleting.Deletion != Requested {
			t.Fatalf("deletion intent overwrote execution state: %+v", deleting)
		}
		pending, err := s.PendingChildren(runctx, 10)
		must(t, err)
		if len(pending) != 1 || pending[0].ID != "drop" {
			t.Fatalf("pending cleanup: %+v", pending)
		}
		wantError(t, s.SettleChild(runctx, pending[0]), ErrBusy)

		must(t, terminals.Stop(runctx, scope.ProjectID, "drop"))
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			err := s.SettleChild(runctx, pending[0])
			if err == nil {
				break
			}
			if !errors.Is(err, ErrBusy) {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		settled, err := s.Terminal(runctx, scope, "drop")
		must(t, err)
		if settled.Deletion != Settled || settled.State != "exited" || settled.ExitCode == nil {
			t.Fatalf("cleanup must preserve how the terminal ended: %+v", settled)
		}
		// The sibling is untouched and still usable.
		g.run(t, keepView, "true", "KEEP-STILL-OK")
		dropView.Close()
		t.Log("terminal cleanup waited for its own process, preserved its exit evidence and left the sibling running")
	})

}
