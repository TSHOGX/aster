package product

import (
	"database/sql"
	"testing"
)

func TestProjectPauseAndRecoveryRetainOnlyActiveOwnerReduction(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
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
	wantError(t, l.Resume(ctx, scope, testProfile().Bindings()), ErrForbidden)
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

func TestExplicitResumeChecksGrantBeforeProvisioning(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	fixtureFileGrants(t, l.Store, map[ProductAction]PolicyEffect{ActionProjectsManage: PolicyDeny})
	wantError(t, l.Resume(ctx, scope, testProfile().Bindings()), ErrForbidden)
	var count int
	must(t, l.Store.reader.QueryRow(`SELECT count(*) FROM project_resources`).Scan(&count))
	if count != 0 {
		t.Fatal("denied resume allocated resources", count)
	}
	fixtureFileGrants(t, l.Store, map[ProductAction]PolicyEffect{ActionProjectsManage: PolicyAllow})
	must(t, l.Resume(ctx, scope, testProfile().Bindings()))
}

func TestNotificationScopeUsesCurrentCanonicalAccount(t *testing.T) {
	s, scope, _ := fixture(t)
	must(t, s.Notify(ctx, scope.Actor, "Notice", "body"))
	wantError(t, s.Notify(ctx, "fixture-"+scope.Actor, "Wrong identity", "body"), ErrNotFound)
	notices, err := s.Notifications(ctx, scope.Actor, "", 10)
	must(t, err)
	if len(notices) != 1 {
		t.Fatal(notices)
	}
	_, err = s.Notifications(ctx, "missing-account", "", 10)
	wantError(t, err, ErrUnauthorized)
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=?`, scope.Actor)
		return err
	}))
	_, err = s.Notifications(ctx, scope.Actor, "", 10)
	wantError(t, err, ErrForbidden)
	_, err = s.UnreadNotifications(ctx, scope.Actor)
	wantError(t, err, ErrForbidden)
	wantError(t, s.ReadNotification(ctx, scope.Actor, notices[0].ID), ErrForbidden)
}
