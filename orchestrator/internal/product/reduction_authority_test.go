package product

import (
	"database/sql"
	"testing"
)

func TestProjectPauseAndRecoveryRetainOnlyActiveOwnerReduction(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	must(t, l.EnsureActive(ctx, scope, testProfile()))
	drive(t, l, scope.ProjectID)
	_, err := s.CreateSession(ctx, scope, "blocked", "Blocked")
	must(t, err)
	_, err = s.db.Exec(`UPDATE sessions SET state='blocked' WHERE id='blocked'`)
	must(t, err)
	child, err := s.Session(ctx, scope, "blocked")
	must(t, err)
	p, err := s.Project(ctx, scope)
	must(t, err)
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionProjectsManage: PolicyDeny, ActionAgentsRun: PolicyDeny, ActionProjectsRead: PolicyDeny})
	wantError(t, l.Restart(ctx, scope, p.Revision), ErrForbidden)
	wantError(t, l.ForceRestart(ctx, scope, p.Revision), ErrForbidden)
	must(t, s.RetryRecovery(ctx, scope, child.ID, child.Revision))
	child, err = sessionIn(ctx, s.reader, scope, child.ID)
	must(t, err)
	must(t, s.ContinueSession(ctx, scope, child.ID, child.Revision))
	_, err = s.Blockers(ctx, scope)
	must(t, err)
	must(t, l.Pause(ctx, scope, p.Revision))
	drive(t, l, scope.ProjectID)
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=?`, scope.Actor)
		return err
	}))
	wantError(t, l.Pause(ctx, scope, p.Revision), ErrForbidden)
	wantError(t, l.Restart(ctx, scope, p.Revision), ErrForbidden)
	wantError(t, l.ForceRestart(ctx, scope, p.Revision), ErrForbidden)
	wantError(t, s.ContinueSession(ctx, scope, child.ID, child.Revision), ErrForbidden)
	wantError(t, s.RetryRecovery(ctx, scope, child.ID, child.Revision), ErrForbidden)
	_, err = s.Blockers(ctx, scope)
	wantError(t, err, ErrForbidden)
	foreign := Scope{Actor: "bob", ProjectID: scope.ProjectID}
	wantError(t, l.Pause(ctx, foreign, p.Revision), ErrNotFound)
	wantError(t, s.RetryRecovery(ctx, foreign, child.ID, child.Revision), ErrNotFound)
}
