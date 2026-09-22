package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestRuntimeAdmissionSharesThePersistedDrainFence(t *testing.T) {
	s, _, _ := fixture(t)
	first, err := s.beginRuntimeWork(ctx, runtimeTitle)
	must(t, err)
	state, err := s.BeginReleaseDrain(ctx, "runtime-fence", time.Minute)
	must(t, err)
	_, err = s.beginRuntimeWork(ctx, runtimeTitle)
	wantError(t, err, ErrDraining)
	_, err = s.beginRuntimeWork(ctx, runtimeInput)
	wantError(t, err, ErrDraining)
	read, err := s.beginRuntimeWork(ctx, runtimeRead)
	must(t, err)
	if got := s.RuntimeWorkStats(); got["title"] != 1 || got["workspace_read"] != 1 {
		t.Fatal(got)
	}
	first()
	first()
	read()
	if len(s.RuntimeWorkStats()) != 0 {
		t.Fatal(s.RuntimeWorkStats())
	}
	_, err = s.AbortReleaseDrain(ctx, state.Owner, state.Generation)
	must(t, err)
	next, err := s.beginRuntimeWork(ctx, runtimeInput)
	must(t, err)
	next()
}

func TestRuntimeAdmissionIsBoundedAndCancellationDoesNotPretendSettlement(t *testing.T) {
	s, _, _ := fixture(t)
	cancelCtx, cancel := context.WithCancel(ctx)
	releases := []func(){}
	for n := 0; n < 8; n++ {
		release, err := s.beginRuntimeWork(cancelCtx, runtimeTitle)
		must(t, err)
		releases = append(releases, release)
	}
	_, err := s.beginRuntimeWork(ctx, runtimeTitle)
	wantError(t, err, ErrCapacity)
	cancel()
	if s.RuntimeWorkStats()["title"] != 8 {
		t.Fatal("cancellation released unfinished operations")
	}
	for _, release := range releases {
		release()
	}
	if len(s.RuntimeWorkStats()) != 0 {
		t.Fatal(s.RuntimeWorkStats())
	}
}

type blockedTitle struct {
	entered chan struct{}
	finish  chan struct{}
}

func (g blockedTitle) GenerateTitle(context.Context, string) (string, error) {
	close(g.entered)
	<-g.finish
	return "Generated title", nil
}
func TestTitleWorkCannotRaceDrainClosure(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "title-session", "")
	must(t, err)
	_, err = s.AdmitTurn(ctx, scope, "title-session", "first", "generate a title", false)
	must(t, err)
	generator := blockedTitle{make(chan struct{}), make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-generator.finish:
		default:
			close(generator.finish)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := s.GenerateTitles(ctx, generator, 1); done <- err }()
	<-generator.entered
	_, err = s.BeginReleaseDrain(ctx, "title-drain", time.Minute)
	must(t, err)
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.RuntimeWork["title"] != 1 {
		t.Fatal(status)
	}
	close(generator.finish)
	must(t, <-done)
	if len(s.RuntimeWorkStats()) != 0 {
		t.Fatal(s.RuntimeWorkStats())
	}
	_, err = s.CreateSession(ctx, scope, "second-title", "")
	must(t, err)
	// A title pending from earlier execution must not start another model request.
	_, err = s.db.Exec(`UPDATE sessions SET title_source='pending' WHERE id='title-session'`)
	must(t, err)
	_, err = s.GenerateTitles(ctx, generator, 1)
	wantError(t, err, ErrDraining)
}

func TestCapturedReadLeaseLastsThroughBodyClose(t *testing.T) {
	files, scope, backend := fileFixture(t)
	files.CaptureDirectory = t.TempDir()
	must(t, os.WriteFile(filepath.Join(backend.root, "lease.txt"), []byte("retained bytes"), 0600))
	capture, err := files.Capture(ctx, scope, "lease.txt")
	must(t, err)
	_, err = files.Store.BeginReleaseDrain(ctx, "read-drain", time.Minute)
	must(t, err)
	if files.Store.RuntimeWorkStats()["workspace_read"] != 1 {
		t.Fatal(files.Store.RuntimeWorkStats())
	}
	_, err = files.List(ctx, scope, "", "", 100)
	must(t, err)
	if files.Store.RuntimeWorkStats()["workspace_read"] != 1 {
		t.Fatal("listing retained lease", files.Store.RuntimeWorkStats())
	}
	must(t, capture.Close())
	must(t, capture.Close())
	if len(files.Store.RuntimeWorkStats()) != 0 {
		t.Fatal(files.Store.RuntimeWorkStats())
	}
}

type blockedInput struct {
	*fakePTY
	entered chan struct{}
	finish  chan struct{}
	once    sync.Once
}

func (b *blockedInput) SendPTYInput(ctx context.Context, ref sandbox.ProcessReference, data []byte) error {
	first := false
	b.once.Do(func() { first = true })
	if first {
		close(b.entered)
		<-b.finish
	}
	return b.fakePTY.SendPTYInput(ctx, ref, data)
}
func TestTerminalInputRemainsCountedUntilBackendReturns(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	blocked := &blockedInput{fakePTY: backend, entered: make(chan struct{}), finish: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-blocked.finish:
		default:
			close(blocked.finish)
		}
	})
	terminals.Backend = blocked
	i := startTerminal(t, terminals, backend, scope, "lease-terminal", 24, 80)
	observer, err := terminals.Attach(ctx, scope, "lease-terminal", true)
	must(t, err)
	defer observer.Close()
	done := make(chan error, 1)
	go func() { done <- terminals.Input(ctx, observer, []byte("work\r")) }()
	<-blocked.entered
	_, err = terminals.Store.BeginReleaseDrain(ctx, "input-drain", time.Minute)
	must(t, err)
	if terminals.Store.RuntimeWorkStats()["terminal_input"] != 1 {
		t.Fatal(terminals.Store.RuntimeWorkStats())
	}
	wantError(t, terminals.Input(ctx, observer, []byte("new\r")), ErrDraining)
	close(blocked.finish)
	must(t, <-done)
	must(t, terminals.Interrupt(ctx, observer))
	must(t, terminals.ObserverResize(ctx, scope, observer, 30, 100))
	must(t, terminals.ObserverSignal(ctx, observer, 15))
	if len(terminals.Store.RuntimeWorkStats()) != 0 {
		t.Fatal(terminals.Store.RuntimeWorkStats())
	}
	backend.mu.Lock()
	input := string(backend.input[i.Reference.Handle])
	backend.mu.Unlock()
	if input != "work\r\x03" {
		t.Fatal(input)
	}
}

func TestRuntimeAdmissionAndDrainRace(t *testing.T) {
	s, _, _ := fixture(t)
	var group sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < 16; n++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			release, err := s.beginRuntimeWork(ctx, runtimeInput)
			if err == nil {
				release()
			} else if !errors.Is(err, ErrDraining) {
				t.Error(err)
			}
		}()
	}
	close(start)
	_, err := s.BeginReleaseDrain(ctx, "racing-runtime", time.Minute)
	must(t, err)
	group.Wait()
	_, err = s.beginRuntimeWork(ctx, runtimeInput)
	wantError(t, err, ErrDraining)
	if len(s.RuntimeWorkStats()) != 0 {
		t.Fatal(s.RuntimeWorkStats())
	}
}
