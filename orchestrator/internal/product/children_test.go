package product

import (
	"testing"
	"time"
)

func TestChildCleanupRequiresEvidenceAndReleasesProjectWork(t *testing.T) {
	s, scope, i, c := agentServiceFixture(t)
	_, _, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	must(t, s.GrantConversation(ctx, scope, i.SessionID, "reader"))
	must(t, s.CanReadConversation(ctx, "reader", i.SessionID))
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.DeleteSession(ctx, scope, i.SessionID, child.Revision))

	// Deletion records intent and closes admission, but does not end a process.
	// Until the runner's termination evidence arrives, the child still pins work.
	pending, err := s.PendingChildren(ctx, 10)
	must(t, err)
	if len(pending) != 1 || pending[0].ID != i.SessionID || pending[0].Kind != "session" {
		t.Fatal(pending)
	}
	wantError(t, s.SettleChild(ctx, pending[0]), ErrBusy)
	_, err = s.ArmIdle(ctx, scope, time.Second)
	wantError(t, err, ErrBusy)

	// A running turn's outcome cannot be manufactured by deleting its Session.
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='exited' WHERE id=?`, i.ID)
	must(t, err)
	wantError(t, s.SettleChild(ctx, pending[0]), ErrBusy)
	_, err = s.db.Exec(`UPDATE turns SET state='unknown' WHERE id=?`, c.TurnID)
	must(t, err)
	// Unsettled activity is also evidence of live guest work.
	must(t, s.db.QueryRow(`SELECT 1`).Scan(new(int)))
	_, err = s.db.Exec(`INSERT INTO project_activity(id,project_id,session_id,kind,state,detail,created_at,updated_at) VALUES('d',?,?,'descendants','unknown','escaped tool',?,?)`, scope.ProjectID, i.SessionID, s.timestamp(), s.timestamp())
	must(t, err)
	wantError(t, s.SettleChild(ctx, pending[0]), ErrBusy)
	must(t, s.SettleActivity(ctx, scope.ProjectID, "d", "complete"))

	must(t, s.SettleChild(ctx, pending[0]))
	// Settling is idempotent, and a settled child no longer appears as pending.
	must(t, s.SettleChild(ctx, pending[0]))
	pending, err = s.PendingChildren(ctx, 10)
	must(t, err)
	if len(pending) != 0 {
		t.Fatal(pending)
	}
	// Cleanup revoked the child's authority and stopped it pinning Project work.
	grants, err := s.ServiceGrants(ctx, scope)
	must(t, err)
	if len(grants) != 0 {
		t.Fatal(grants)
	}
	wantError(t, s.CanReadConversation(ctx, "reader", i.SessionID), ErrNotFound)
	if _, err = s.Session(ctx, scope, i.SessionID); err != nil {
		t.Fatal("history must stay readable after cleanup", err)
	}
	sessions, err := s.Sessions(ctx, scope, "", 10)
	must(t, err)
	if len(sessions) != 0 {
		t.Fatal(sessions)
	}
	// The Project itself was never paused or reset by its child's deletion.
	p, err := s.Project(ctx, scope)
	must(t, err)
	if p.State != "active" {
		t.Fatal(p)
	}
	_, err = s.ArmIdle(ctx, scope, time.Second)
	must(t, err)
}

func TestTerminalCleanupWaitsForItsProcessAndSpareSiblings(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, scope, testProfile()))
	drive(t, l, scope.ProjectID)
	s := l.Store
	for _, id := range []string{"keep", "drop"} {
		_, err := s.CreateTerminal(ctx, scope, id, "Terminal", 24, 80)
		must(t, err)
	}
	_, err := s.db.Exec(`UPDATE terminals SET state='running' WHERE id IN ('keep','drop')`)
	must(t, err)
	// A live PTY's activity is what pins the Project and blocks cleanup. The
	// Terminal row's own state is not that evidence: recording a deletion intent
	// overwrites it, which is exactly why the process must be tracked separately.
	must(t, s.BeginActivity(ctx, scope, Activity{ID: "pty/drop", TerminalID: "drop", Kind: "terminal_control", Detail: "live PTY"}))
	child, err := s.Terminal(ctx, scope, "drop")
	must(t, err)
	must(t, s.DeleteTerminal(ctx, scope, "drop", child.Revision))
	pending, err := s.PendingChildren(ctx, 10)
	must(t, err)
	if len(pending) != 1 || pending[0].ID != "drop" {
		t.Fatal(pending)
	}
	wantError(t, s.SettleChild(ctx, pending[0]), ErrBusy)
	must(t, s.SettleActivity(ctx, scope.ProjectID, "pty/drop", "complete"))
	must(t, s.SettleChild(ctx, pending[0]))
	// The sibling terminal is untouched and still pins the Project.
	sibling, err := s.Terminal(ctx, scope, "keep")
	must(t, err)
	if sibling.State != "running" {
		t.Fatal(sibling)
	}
	_, err = s.ArmIdle(ctx, scope, time.Second)
	wantError(t, err, ErrBusy)
	wantError(t, s.SettleChild(ctx, Child{ProjectID: scope.ProjectID, ID: "keep", Kind: "terminal"}), ErrConflict)
	wantError(t, s.SettleChild(ctx, Child{ProjectID: scope.ProjectID, ID: "absent", Kind: "terminal"}), ErrNotFound)
	wantError(t, s.SettleChild(ctx, Child{ProjectID: scope.ProjectID, ID: "keep", Kind: "project"}), ErrInvalid)
}
