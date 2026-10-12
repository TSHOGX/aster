package product

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func driveAdminControl(t *testing.T, w *Workers, token, id, project string) AdminOperation {
	t.Helper()
	for n := 0; n < 20; n++ {
		p, err := w.Store.OperatorProject(ctx, project)
		must(t, err)
		err = w.project(ctx, p)
		if err != nil && !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		operation, err := w.Store.AdminOperation(ctx, token, id)
		must(t, err)
		if operation.State == "succeeded" || operation.State == "failed" {
			return operation
		}
	}
	t.Fatal("admin control did not settle")
	return AdminOperation{}
}
func TestAdminProjectControlsRevisionsReceiptsAndRetainedMounts(t *testing.T) {
	l, scope, backend := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	before, err := s.Resources(ctx, scope)
	must(t, err)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	input := AdminProjectControl{RequestID: "admin-stop", ExpectedRevision: p.Revision, InterruptionAcknowledged: true}
	accepted, err := s.AdminControlProject(ctx, token, p.ID, "stop", input)
	must(t, err)
	if accepted.Operation.State != "accepted" || len(backend.creates) != 1 {
		t.Fatal(accepted)
	}
	replay, err := s.AdminControlProject(ctx, token, p.ID, "stop", input)
	must(t, err)
	if !replay.Replayed || replay.Operation.ID != accepted.Operation.ID {
		t.Fatal(replay)
	}
	input.RequestID = "stale"
	_, err = s.AdminControlProject(ctx, token, p.ID, "pause", input)
	wantError(t, err, ErrConflict)
	worker := &Workers{Store: s, Lifecycle: l}
	backend.fail["pause"] = 1
	if _, err = worker.adminProjectStep(ctx, p); err == nil {
		t.Fatal("lost stop hidden")
	}
	outcome, err := s.AdminOperation(ctx, token, accepted.Operation.ID)
	must(t, err)
	if outcome.State != "unknown" {
		t.Fatal(outcome)
	}
	beforeRetry, err := s.Resources(ctx, scope)
	must(t, err)
	if !beforeRetry.Occupied {
		t.Fatal("lost reply released compute")
	}
	var seq int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	must(t, s.Close())
	s, err = openFixtureStore(ctx, path)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	l.Store = s
	worker.Store = s
	outcome = driveAdminControl(t, worker, token, accepted.Operation.ID, p.ID)
	if outcome.State != "succeeded" {
		t.Fatal(outcome)
	}
	stopped, err := s.Resources(ctx, scope)
	must(t, err)
	if stopped.Occupied || !reflect.DeepEqual(stopped.Volumes, before.Volumes) {
		t.Fatal(stopped)
	}
	wantError(t, l.EnsureActive(ctx, scope, testProfile().Bindings()), ErrForbidden)
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	restart, err := s.AdminControlProject(ctx, token, p.ID, "restart", AdminProjectControl{RequestID: "admin-restart", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	must(t, err)
	wantError(t, l.Resume(ctx, scope, testProfile().Bindings()), ErrConflict)
	outcome = driveAdminControl(t, worker, token, restart.Operation.ID, p.ID)
	if outcome.State != "succeeded" {
		t.Fatal(outcome)
	}
	resumed, err := s.Resources(ctx, scope)
	must(t, err)
	if !resumed.Occupied || !reflect.DeepEqual(resumed.Profile.Volumes, before.Profile.Volumes) || resumed.Profile.SandboxRelease != before.Profile.SandboxRelease || !reflect.DeepEqual(resumed.Volumes, before.Volumes) || resumed.Reference.Epoch <= before.Reference.Epoch {
		t.Fatal(resumed)
	}
}
func TestAdminApplyNetworkPinsReviewedSelectionAndRechecksResumeQuotas(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	worker := &Workers{Store: s, Lifecycle: l}
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	network, err := fixtureNetwork(ctx, s.reader)
	must(t, err)
	edited, err := selectFixtureNetwork(t, s, policyFixture(t, "", "192.0.2.53"))
	must(t, err)
	_, err = selectFixtureNetwork(t, s, edited)
	must(t, err)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	_, err = s.AdminControlProject(ctx, token, p.ID, "apply_network", AdminProjectControl{RequestID: "stale-network", ExpectedRevision: p.Revision, ExpectedNetworkRevision: network.Revision, InterruptionAcknowledged: true})
	wantError(t, err, ErrConflict)
	input := AdminProjectControl{RequestID: "apply-network", ExpectedRevision: p.Revision, ExpectedNetworkRevision: edited.Revision, InterruptionAcknowledged: true}
	applied, err := s.AdminControlProject(ctx, token, p.ID, "apply_network", input)
	must(t, err)
	outcome := driveAdminControl(t, worker, token, applied.Operation.ID, p.ID)
	if outcome.State != "succeeded" {
		t.Fatal(outcome)
	}
	r, err := s.Resources(ctx, scope)
	must(t, err)
	if r.Profile.NetworkPolicyRevision != edited.Revision {
		t.Fatal(r)
	}
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	restart, err := s.AdminControlProject(ctx, token, p.ID, "restart", AdminProjectControl{RequestID: "quota-before-resume", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	must(t, err)
	fixtureResourceLimit(t, s, scope.Actor, ResourceCPU, 0)
	_, err = worker.adminProjectStep(ctx, p)
	wantError(t, err, ErrQuotaExceeded)
	outcome, err = s.AdminOperation(ctx, token, restart.Operation.ID)
	must(t, err)
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	if outcome.State != "failed" || p.State != "paused" || !p.ExecutionSuspended {
		t.Fatal(outcome, p)
	}
}
func TestAccountSuspensionSupersedesAnAdminRestart(t *testing.T) {
	l, scope, backend := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	worker := &Workers{Store: s, Lifecycle: l}
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	operation, err := s.AdminControlProject(ctx, token, p.ID, "restart", AdminProjectControl{RequestID: "old-restart", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	must(t, err)
	owner, err := s.Account(ctx, scope.Actor)
	must(t, err)
	suspended, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "supersede", ExpectedRevision: owner.Revision})
	must(t, err)
	_, err = s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable", ExpectedRevision: suspended.User.Revision})
	must(t, err)
	must(t, worker.project(ctx, p))
	must(t, worker.suspensionOperations(ctx))
	old, err := s.AdminOperation(ctx, token, operation.Operation.ID)
	must(t, err)
	if old.State != "failed" || backend.calls["resume"] != 0 {
		t.Fatal(old, backend.calls)
	}
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	if !p.ExecutionSuspended || p.ExecutionHoldReason != "account_suspended" || p.State != "paused" {
		t.Fatal(p)
	}
}
func TestAdminProjectControlAuditFailureIsAtomic(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER project_control_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='project.pause' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	must(t, err)
	_, err = s.AdminControlProject(ctx, token, p.ID, "pause", AdminProjectControl{RequestID: "rollback", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	if err == nil {
		t.Fatal("audit failed open")
	}
	got, err := s.OperatorProject(ctx, p.ID)
	must(t, err)
	if got.ExecutionSuspended || got.Revision != p.Revision {
		t.Fatal(got)
	}
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM admin_operations WHERE target_id=?`, p.ID).Scan(&count))
	if count != 0 {
		t.Fatal(count)
	}
}

func TestAdminRestartWithdrawnDuringResumeStopsBeforeFailure(t *testing.T) {
	l, scope, backend := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	w := &Workers{Store: s, Lifecycle: l}
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	accepted, err := s.AdminControlProject(ctx, token, p.ID, "restart", AdminProjectControl{RequestID: "withdrawn-resume", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	must(t, err)
	_, err = w.adminProjectStep(ctx, p)
	must(t, err)
	continuation, err := adminProjectContinuationFor(ctx, s.reader, p.ID)
	must(t, err)
	if continuation.phase != "resuming" {
		t.Fatal(continuation)
	}
	fixtureResourceLimit(t, s, scope.Actor, ResourceCPU, 0)
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	_, err = w.adminProjectStep(ctx, p)
	must(t, err)
	current, err := s.AdminOperation(ctx, token, accepted.Operation.ID)
	must(t, err)
	if current.State == "failed" {
		t.Fatal("unsettled effects reported final")
	}
	outcome := driveAdminControl(t, w, token, accepted.Operation.ID, p.ID)
	if outcome.State != "failed" || backend.calls["resume"] != 0 {
		t.Fatal(outcome, backend.calls)
	}
}

func TestAdminNetworkChangeDuringStopRequiresNewReviewedRequest(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	w := &Workers{Store: s, Lifecycle: l}
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	network, err := fixtureNetwork(ctx, s.reader)
	must(t, err)
	request, err := s.AdminControlProject(ctx, token, p.ID, "apply_network", AdminProjectControl{RequestID: "reviewed-old-network", ExpectedRevision: p.Revision, ExpectedNetworkRevision: network.Revision, InterruptionAcknowledged: true})
	must(t, err)
	changedNetwork, err := selectFixtureNetwork(t, s, policyFixture(t, "", "192.0.2.53"))
	must(t, err)
	_, err = selectFixtureNetwork(t, s, changedNetwork)
	must(t, err)
	_, err = w.adminProjectStep(ctx, p)
	wantError(t, err, ErrConflict)
	operation, err := s.AdminOperation(ctx, token, request.Operation.ID)
	must(t, err)
	retained, err := s.Resources(ctx, scope)
	must(t, err)
	if operation.State != "failed" || retained.Occupied || retained.Profile.NetworkPolicyRevision != network.Revision {
		t.Fatal(operation, retained)
	}
}
func TestProjectDeletionSupersedesAdminControlAndCleansNormally(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	w := &Workers{Store: s, Lifecycle: l}
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	request, err := s.AdminControlProject(ctx, token, p.ID, "restart", AdminProjectControl{RequestID: "restart-before-delete", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	must(t, err)
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	p, err = s.OperatorProject(ctx, p.ID)
	must(t, err)
	handled, err := w.adminProjectStep(ctx, p)
	must(t, err)
	if handled {
		t.Fatal("deletion cleanup hidden by stale control")
	}
	drive(t, l, p.ID)
	result, err := s.AdminOperation(ctx, token, request.Operation.ID)
	must(t, err)
	if result.State != "failed" {
		t.Fatal(result)
	}
	usage, err := storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) {
		t.Fatal(usage)
	}
}

func TestAdministrativeHoldDefersScheduleDispatchButNotDeadline(t *testing.T) {
	s, scope, _ := fixture(t)
	token := suspensionAdmin(t, s)
	originalNow := s.now
	now := originalNow()
	s.now = func() time.Time { return now }
	task, err := s.CreateSchedule(ctx, scope, Schedule{ID: "controlled-schedule", Title: "Later", Prompt: "Work", TriggerKind: "interval", Cron: "@every 1m", Timezone: "UTC", State: "active", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano)})
	must(t, err)
	p, err := s.OperatorProject(ctx, scope.ProjectID)
	must(t, err)
	_, err = s.AdminControlProject(ctx, token, p.ID, "pause", AdminProjectControl{RequestID: "pause-schedule-target", ExpectedRevision: p.Revision, InterruptionAcknowledged: true})
	must(t, err)
	now = now.Add(2 * time.Minute)
	count, err := s.TickSchedules(ctx, 10)
	must(t, err)
	if count != 0 {
		t.Fatal("schedule dispatched during hold")
	}
	current, err := s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.State != "active" {
		t.Fatal("temporary hold changed schedule definition", current)
	}
	now = now.Add(time.Hour)
	count, err = s.TickSchedules(ctx, 10)
	must(t, err)
	current, err = s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if count != 0 || current.State != "exhausted" {
		t.Fatal("hold prevented independent expiration", count, current)
	}
}
