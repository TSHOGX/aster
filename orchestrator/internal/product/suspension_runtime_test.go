package product

import (
	"context"
	"errors"
	"testing"
)

func suspensionAdmin(t *testing.T, s *Store) string {
	t.Helper()
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id='bob'`)
	must(t, err)
	a, err := s.Account(ctx, "bob")
	must(t, err)
	_, token, err := s.Authenticate(ctx, a.Username, "fixture-password")
	must(t, err)
	return token
}
func TestSuspensionObservedPauseUnknownRetryAndExplicitResume(t *testing.T) {
	l, scope, backend := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	original, err := s.Resources(ctx, scope)
	must(t, err)
	owner, err := s.Account(ctx, scope.Actor)
	must(t, err)
	suspended, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "stop-running", ExpectedRevision: owner.Revision})
	must(t, err)
	_, err = s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable-early", ExpectedRevision: suspended.User.Revision})
	must(t, err)
	wantError(t, l.EnsureActive(ctx, scope, testProfile().Bindings()), ErrForbidden)
	wantError(t, l.Resume(ctx, scope, testProfile().Bindings()), ErrConflict)
	worker := &Workers{Store: s, Lifecycle: l}
	backend.fail["pause"] = 1
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	if err = worker.project(ctx, p); err == nil {
		t.Fatal("lost pause reply falsely confirmed")
	}
	must(t, worker.suspensionOperations(ctx))
	operation, err := s.AdminOperation(ctx, token, suspended.Operation.ID)
	must(t, err)
	if operation.State != "unknown" || operation.Completed != 0 {
		t.Fatal(operation)
	}
	still, err := s.Resources(ctx, scope)
	must(t, err)
	if !still.Occupied {
		t.Fatal("timeout released compute")
	}
	// Owner replacement must continue the original operation and hold.
	var seq int
	var name, dbPath string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbPath))
	must(t, s.Close())
	reopened, e := openFixtureStore(ctx, dbPath)
	must(t, e)
	t.Cleanup(func() { reopened.Close() })
	s = reopened
	l.Store = reopened
	worker.Store = reopened
	// Retry observes the same paused binding; it does not create or resume.
	must(t, worker.project(ctx, p))
	must(t, worker.suspensionOperations(ctx))
	operation, err = s.AdminOperation(ctx, token, suspended.Operation.ID)
	must(t, err)
	if operation.State != "succeeded" || operation.Completed != 1 {
		t.Fatal(operation)
	}
	paused, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	if !paused.ExecutionSuspended || paused.State != "paused" {
		t.Fatal(paused)
	}
	got, err := s.Resources(ctx, scope)
	must(t, err)
	if got.Occupied || got.VolumeForRole("workspace") != original.VolumeForRole("workspace") {
		t.Fatal(got)
	}
	before := backend.calls["pause"]
	must(t, worker.project(ctx, paused))
	must(t, worker.suspensionOperations(ctx))
	if backend.calls["pause"] != before {
		t.Fatal("completed suspension repeated effects")
	}
	wantError(t, l.EnsureActive(ctx, scope, testProfile().Bindings()), ErrForbidden)
	must(t, l.Resume(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	resumed, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	if resumed.ExecutionSuspended || resumed.State != "active" {
		t.Fatal(resumed)
	}
	var audit int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action='account.suspend.completed'`).Scan(&audit))
	if audit != 1 {
		t.Fatal(audit)
	}
}
func TestSuspensionPartialPlanKeepsOriginalSlotsAndNoAutomaticProvisioning(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "undispatched", true: "lost-slot-reply"}[lost], func(t *testing.T) {
			l, scope, backend := lifecycleFixture(t)
			s := l.Store
			token := suspensionAdmin(t, s)
			must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
			if lost {
				backend.fail["volume"] = 1
				if err := l.Reconcile(ctx, scope.ProjectID); err == nil {
					t.Fatal("lost reply not injected")
				}
			}
			original, err := s.Resources(ctx, scope)
			must(t, err)
			owner, err := s.Account(ctx, scope.Actor)
			must(t, err)
			suspended, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "partial-stop", ExpectedRevision: owner.Revision})
			must(t, err)
			calls := backend.calls["volume"]
			worker := &Workers{Store: s, Lifecycle: l}
			p, err := s.OperatorProject(ctx, scope.ProjectID)
			must(t, err)
			must(t, worker.project(ctx, p))
			must(t, worker.suspensionOperations(ctx))
			if backend.calls["volume"] != calls || len(backend.creates) != 0 {
				t.Fatal("suspension materialized another resource")
			}
			operation, err := s.AdminOperation(ctx, token, suspended.Operation.ID)
			must(t, err)
			if operation.State != "succeeded" {
				t.Fatal(operation)
			}
			retained, err := s.Resources(ctx, scope)
			must(t, err)
			if retained.Occupied || retained.Profile.EnvironmentRevision != original.Profile.EnvironmentRevision {
				t.Fatal(retained)
			}
			usage, err := storageUsage(ctx, s.reader, scope.Actor)
			must(t, err)
			if usage.Reserved != 96<<20 {
				t.Fatal("partial storage reservation lost", usage)
			}
			_, err = s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable-partial", ExpectedRevision: suspended.User.Revision})
			must(t, err)
			wantError(t, l.EnsureActive(ctx, scope, testProfile().Bindings()), ErrForbidden)
			must(t, l.Resume(ctx, scope, testProfile().Bindings()))
			drive(t, l, scope.ProjectID)
			if len(backend.volumes) != 2 || len(backend.creates) != 1 {
				t.Fatal("retry duplicated slot resource", backend.calls)
			}
		})
	}
}
func TestSuspensionSettlesOriginalLostCreateWithoutAdmittingGuestWork(t *testing.T) {
	l, scope, backend := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	for {
		o, err := pendingOperation(ctx, s.reader, scope.ProjectID)
		must(t, err)
		if o.Phase == "sandbox" {
			break
		}
		must(t, l.Reconcile(ctx, scope.ProjectID))
	}
	backend.fail["create"] = 1
	if err := l.Reconcile(ctx, scope.ProjectID); err == nil {
		t.Fatal("lost create not injected")
	}
	owner, err := s.Account(ctx, scope.Actor)
	must(t, err)
	suspended, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "unknown-create", ExpectedRevision: owner.Revision})
	must(t, err)
	worker := &Workers{Store: s, Lifecycle: l}
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	must(t, worker.project(ctx, p))
	must(t, worker.suspensionOperations(ctx))
	if len(backend.creates) != 1 || len(backend.projections) != 0 {
		t.Fatal("created replacement or projected runnable guest", backend.calls)
	}
	operation, err := s.AdminOperation(ctx, token, suspended.Operation.ID)
	must(t, err)
	if operation.State != "succeeded" {
		t.Fatal(operation)
	}
	retained, err := s.Resources(ctx, scope)
	must(t, err)
	if retained.Occupied || backend.sandboxes[retained.Reference.SandboxID].State != "paused" {
		t.Fatal(retained)
	}
}
func TestSuspensionNoProjectCompletionAndLastAdminRemainProtected(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	created, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "empty-user", Username: "empty", DisplayName: "Empty", Role: "user"})
	must(t, err)
	suspended, err := s.SuspendAccount(ctx, token, created.User.ID, AccountRevision{RequestID: "empty-suspend", ExpectedRevision: created.User.Revision})
	must(t, err)
	worker := &Workers{Store: s}
	must(t, worker.suspensionOperations(ctx))
	operation, err := s.AdminOperation(ctx, token, suspended.Operation.ID)
	must(t, err)
	if operation.State != "succeeded" || operation.Total != 0 {
		t.Fatal(operation)
	}
	_, err = s.SuspendAccount(context.Background(), token, admin.ID, AccountRevision{RequestID: "last-admin-suspend", ExpectedRevision: admin.Revision})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestSuspensionTargetsAreScopedAndBounded(t *testing.T) {
	s, scope, _ := fixture(t)
	token := suspensionAdmin(t, s)
	_, err := s.CreateProject(ctx, scope.Actor, "second-target", "Second")
	must(t, err)
	user, err := s.Account(ctx, scope.Actor)
	must(t, err)
	mutation, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "targets", ExpectedRevision: user.Revision})
	must(t, err)
	first, err := s.AdminOperationTargets(ctx, token, mutation.Operation.ID, "", 1)
	must(t, err)
	if len(first.Targets) != 1 || first.NextCursor == "" || first.Targets[0].State != "accepted" {
		t.Fatal(first)
	}
	second, err := s.AdminOperationTargets(ctx, token, mutation.Operation.ID, first.NextCursor, 1)
	must(t, err)
	if len(second.Targets) != 1 || second.Targets[0].ProjectID == first.Targets[0].ProjectID || second.NextCursor != "" {
		t.Fatal(second)
	}
	enabled, err := s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable-targets", ExpectedRevision: mutation.User.Revision})
	must(t, err)
	_, err = s.AdminOperationTargets(ctx, token, enabled.Operation.ID, first.NextCursor, 1)
	wantError(t, err, ErrInvalid)
	ordinary, err := s.Account(ctx, scope.Actor)
	must(t, err)
	_, userToken, err := s.Authenticate(ctx, ordinary.Username, "fixture-password")
	must(t, err)
	_, err = s.AdminOperationTargets(ctx, userToken, mutation.Operation.ID, "", 1)
	wantError(t, err, ErrForbidden)
}
