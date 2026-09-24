package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

func TestReleaseReadyChecksRuntimeAndDurableWorkAtomically(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "session", "Session")
	must(t, err)
	lease, err := s.beginRuntimeWork(ctx, runtimeRead)
	must(t, err)
	drain, err := s.BeginReleaseDrain(ctx, "ready-owner", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
	lease()
	ready, err := s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	if ready.Phase != "ready" {
		t.Fatal(ready)
	}
	_, err = s.beginRuntimeWork(ctx, runtimeRead)
	wantError(t, err, ErrDraining)
	_, err = s.TryReleaseReady(ctx, "stale", drain.Generation)
	wantError(t, err, ErrConflict)
	_, err = s.AdmitTurn(ctx, scope, "session", "held", "held intent", true)
	must(t, err)
	still, err := s.ReleaseState(ctx)
	must(t, err)
	if still.Phase != "ready" {
		t.Fatal(still)
	}
	// Required cleanup invalidates ready in the same transaction as its evidence.
	must(t, s.BeginActivity(ctx, scope, Activity{ID: "late-cleanup", SessionID: "session", Kind: "descendants", Detail: "unsettled descendants"}))
	changed, err := s.ReleaseState(ctx)
	must(t, err)
	if changed.Phase != "draining" || changed.Generation != ready.Generation || changed.Reason != "work_resumed" {
		t.Fatal(changed)
	}
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
	must(t, s.SettleActivity(ctx, scope.ProjectID, "late-cleanup", "complete"))
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	_, err = s.AbortReleaseDrain(ctx, drain.Owner, drain.Generation)
	must(t, err)
}

func TestReleaseReadySurvivesRestartAndDeadlineAbortsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.db")
	s, err := Open(ctx, path)
	must(t, err)
	drain, err := s.BeginReleaseDrain(ctx, "ready-restart", time.Minute)
	must(t, err)
	ready, err := s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	restored, err := s.ReleaseState(ctx)
	must(t, err)
	if restored != ready {
		t.Fatal(restored, ready)
	}
	deadline, err := time.Parse(time.RFC3339Nano, drain.Deadline)
	must(t, err)
	s.now = func() time.Time { return deadline.Add(time.Second) }
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrReleaseExpired)
	state, err := s.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "open" || state.Reason != "deadline" {
		t.Fatal(state)
	}
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrConflict)
}

func TestReleaseReadyCannotRaceReaderAdmission(t *testing.T) {
	for round := 0; round < 20; round++ {
		s, _, _ := fixture(t)
		drain, err := s.BeginReleaseDrain(ctx, fmt.Sprint("race-", round), time.Minute)
		must(t, err)
		start := make(chan struct{})
		var group sync.WaitGroup
		var lease func()
		var leaseErr, readyErr error
		group.Add(2)
		go func() { defer group.Done(); <-start; lease, leaseErr = s.beginRuntimeWork(ctx, runtimeRead) }()
		go func() {
			defer group.Done()
			<-start
			_, readyErr = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
		}()
		close(start)
		group.Wait()
		if leaseErr == nil {
			if !errors.Is(readyErr, ErrBusy) {
				t.Fatal("ready crossed live lease", readyErr)
			}
			lease()
		} else {
			if !errors.Is(leaseErr, ErrDraining) || readyErr != nil {
				t.Fatal(leaseErr, readyErr)
			}
		}
	}
}

func TestAuthorizedTerminalCancellationRevokesReady(t *testing.T) {
	terminals, backend, scope := terminalFixture(t)
	startTerminal(t, terminals, backend, scope, "shell", 24, 80)
	observer, err := terminals.Attach(ctx, scope, "shell", true)
	must(t, err)
	defer observer.Close()
	drain, err := terminals.Store.BeginReleaseDrain(ctx, "terminal-ready", time.Minute)
	must(t, err)
	_, err = terminals.Store.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	wantError(t, terminals.Signal(ctx, Scope{Actor: "foreign", ProjectID: scope.ProjectID}, "shell", 15), ErrNotFound)
	state, err := terminals.Store.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "ready" {
		t.Fatal("denied signal revoked readiness", state)
	}
	must(t, terminals.Interrupt(ctx, observer))
	state, err = terminals.Store.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "draining" || state.Reason != "cancellation_requested" {
		t.Fatal(state)
	}
}

func TestProjectDeletionAndLateUnknownEvidenceRevokeReady(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	ingestContract(t, s, i, c, "native", consumedPayload(c))
	_, err := s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE prompt_id=?`, c.TurnID)
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_commands SET state='completed' WHERE id=?`, c.CommandID)
	must(t, err)
	drain, err := s.BeginReleaseDrain(ctx, "late-evidence", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='unknown' WHERE id=?`, i.ID)
	must(t, err)
	state, err := s.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "draining" {
		t.Fatal(state)
	}
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, i.ID)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	p, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
}

func TestReadinessUsesUnsettledIndexes(t *testing.T) {
	s, _, _ := fixture(t)
	rows, err := s.reader.Query(`EXPLAIN QUERY PLAN SELECT EXISTS(SELECT 1 FROM release_blockers)`)
	must(t, err)
	defer rows.Close()
	indexes := map[string]bool{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		must(t, rows.Scan(&id, &parent, &unused, &detail))
		t.Log(detail)
		if strings.Contains(detail, "release_") && strings.Contains(detail, "USING") {
			indexes[detail] = true
		}
	}
	must(t, rows.Err())
	if len(indexes) < 13 {
		t.Fatal("readiness did not use all unsettled indexes", indexes)
	}
}

// Any direct update introducing a blocker must invalidate a ready generation,
// independently of which worker or API method issued it.
func TestReadyRevocationRollsBackWithItsEvidence(t *testing.T) {
	s, scope, _ := fixture(t)
	drain, err := s.BeginReleaseDrain(ctx, "rollback-ready", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	injected := errors.New("rollback cleanup intent")
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(context.Background(), `UPDATE projects SET state='deleting' WHERE id=?`, scope.ProjectID)
		if e != nil {
			return e
		}
		return injected
	})
	wantError(t, err, injected)
	state, err := s.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "ready" {
		t.Fatal(state)
	}
}

type blockedReaderStart struct {
	*localFileBackend
	entered chan struct{}
	finish  chan struct{}
}

func (b *blockedReaderStart) StartProcess(ctx context.Context, request sandbox.StartProcessRequest) (sandbox.ProcessStream, error) {
	close(b.entered)
	<-b.finish
	return b.localFileBackend.StartProcess(ctx, request)
}
func TestCanceledReadDoesNotHideItsStartingHelper(t *testing.T) {
	files, scope, backend := fileFixture(t)
	files.CaptureDirectory = t.TempDir()
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("bytes"), 0600))
	blocked := &blockedReaderStart{backend, make(chan struct{}), make(chan struct{})}
	files.Backend = blocked
	t.Cleanup(func() {
		select {
		case <-blocked.finish:
		default:
			close(blocked.finish)
		}
	})
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := files.Capture(readCtx, scope, "file"); done <- err }()
	<-blocked.entered
	cancel()
	wantError(t, <-done, context.Canceled)
	drain, err := files.Store.BeginReleaseDrain(ctx, "starting-reader", time.Minute)
	must(t, err)
	_, err = files.Store.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
	if stats := files.Store.RuntimeWorkStats(); stats["reader_start"] != 1 || stats["workspace_read"] != 0 {
		t.Fatal(stats)
	}
	close(blocked.finish)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if files.Store.RuntimeWorkStats()["reader_start"] == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_, err = files.Store.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
}

func TestReadinessHonorsExplicitAcknowledgmentWithoutErasingUnknownEvidence(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	for _, query := range []string{
		`UPDATE agent_commands SET state='unknown'`,
		`UPDATE execution_runs SET state='unknown'`,
		`UPDATE agent_incarnations SET state='exited',cleanup_state='settled',recovery_state='failed'`,
		`UPDATE sessions SET state='blocked'`,
	} {
		_, err := s.db.Exec(query)
		must(t, err)
	}
	drain, err := s.BeginReleaseDrain(ctx, "acknowledged-history", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.ContinueSession(ctx, scope, i.SessionID, child.Revision))
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	var outcome string
	must(t, s.reader.QueryRow(`SELECT state FROM agent_commands WHERE id=?`, c.CommandID).Scan(&outcome))
	if outcome != "unknown" {
		t.Fatal("acknowledgment rewrote outcome", outcome)
	}
	_, err = s.db.Exec(`UPDATE agent_incarnations SET cleanup_state='unknown' WHERE id=?`, i.ID)
	must(t, err)
	state, err := s.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "draining" {
		t.Fatal(state)
	}
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
}
