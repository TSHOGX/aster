package product

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestTerminalPolicyRevocationClosesViewsAndRetainsReduction(t *testing.T) {
	for _, notify := range []bool{true, false} {
		t.Run(map[bool]string{true: "commit-notification", false: "poll-fallback"}[notify], func(t *testing.T) {
			terminals, backend, scope := terminalFixture(t)
			s := terminals.Store
			i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)
			writer, err := terminals.Attach(ctx, scope, "shell", true)
			must(t, err)
			reader, err := terminals.Attach(ctx, scope, "shell", false)
			must(t, err)
			mutate := func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `UPDATE permission_policies SET statements=json_set(statements,'$."terminals.use"','deny') WHERE id='fixture-policy'`)
				return err
			}
			start := time.Now()
			if notify {
				must(t, s.writeIdentity(ctx, mutate))
			} else {
				must(t, s.write(ctx, mutate))
			}
			_, err = terminals.Start(ctx, scope, "shell", 24, 80)
			wantError(t, err, ErrForbidden)
			_, err = terminals.Attach(ctx, scope, "shell", true)
			wantError(t, err, ErrForbidden)
			_, err = s.reserveTerminal(ctx, scope, "shell", 24, 80)
			wantError(t, err, ErrForbidden)
			wantError(t, terminals.Resize(ctx, scope, "shell", 40, 120), ErrForbidden)
			// A concurrent observer close may surface as conflict/not-found instead of
			// denial. None of these controls may reach the retained PTY after revocation.
			for _, err := range []error{terminals.Input(ctx, writer, []byte("denied")), terminals.ClaimInput(ctx, reader), terminals.ObserverResize(ctx, scope, reader, 40, 120)} {
				if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
					t.Fatal("interactive control admitted", err)
				}
			}
			for _, o := range []*TerminalObserver{writer, reader} {
				deadline := time.NewTimer(time.Until(start.Add(5 * time.Second)))
				defer deadline.Stop()
			closed:
				for {
					select {
					case _, ok := <-o.Frames():
						if !ok {
							break closed
						}
					case <-deadline.C:
						t.Fatal("terminal policy revocation missed five-second bound")
					}
				}
			}
			backend.mu.Lock()
			input, signals, size := len(backend.input[i.Reference.Handle]), len(backend.signals[i.Reference.Handle]), backend.sizes[i.Reference.Handle]
			backend.mu.Unlock()
			if input != 0 || signals != 0 || size != [2]int{24, 80} {
				t.Fatal("revocation affected process", input, signals, size)
			}
			// Outcome inspection and explicit termination retain active-owner authority.
			live, err := s.LiveTerminal(ctx, scope, "shell")
			must(t, err)
			if live.State != "running" {
				t.Fatal(live)
			}
			wantError(t, terminals.Signal(ctx, scope, "shell", 18), ErrInvalid)
			must(t, terminals.Signal(ctx, scope, "shell", 15))
			must(t, terminals.EOF(ctx, scope, "shell"))
			backend.stream(i.Reference.Handle).frames <- sandbox.ProcessFrame{Kind: "end", ExitCode: 0}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				_, err = s.LiveTerminal(ctx, scope, "shell")
				if errors.Is(err, ErrNotFound) {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("revocation prevented exit settlement", err)
		})
	}
}

func TestTerminalGrantIsIndependentOfProjectRead(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	s := terminals.Store
	startTerminal(t, terminals, backend, scope, "shell", 24, 80)
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE permission_policies SET statements=json_set(statements,'$."projects.read"','deny') WHERE id='fixture-policy'`)
		return err
	}))
	_, err := s.Project(ctx, scope)
	wantError(t, err, ErrForbidden)
	o, err := terminals.Attach(ctx, scope, "shell", true)
	must(t, err)
	defer o.Close()
	must(t, terminals.Input(ctx, o, []byte("allowed")))
	must(t, terminals.ObserverResize(ctx, scope, o, 30, 90))
	foreign := Scope{Actor: "bob", ProjectID: scope.ProjectID}
	_, err = terminals.Attach(ctx, foreign, "shell", false)
	wantError(t, err, ErrNotFound)
}

func TestOwnedTerminalStopDoesNotRequireReadOrUse(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	terminals.TerminationGrace = time.Millisecond
	i := startTerminal(t, terminals, backend, scope, "shell", 24, 80)
	fixtureFileGrants(t, terminals.Store, map[ProductAction]PolicyEffect{ActionProjectsRead: PolicyDeny, ActionTerminalsUse: PolicyDeny})
	must(t, terminals.StopOwned(ctx, scope, "shell"))
	backend.mu.Lock()
	signals := append([]int32(nil), backend.signals[i.Reference.Handle]...)
	backend.mu.Unlock()
	if len(signals) != 2 || signals[0] != 15 || signals[1] != 9 {
		t.Fatal("owned stop lost reduction authority", signals)
	}
	wantError(t, terminals.StopOwned(ctx, Scope{Actor: "bob", ProjectID: scope.ProjectID}, "shell"), ErrNotFound)
	must(t, terminals.Store.writeIdentity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=?`, scope.Actor)
		return err
	}))
	wantError(t, terminals.StopOwned(ctx, scope, "shell"), ErrForbidden)
}
