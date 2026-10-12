package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/testfixture"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureResourceLimit(t *testing.T, s *Store, actor string, dimension ResourceDimension, value int64) {
	t.Helper()
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_resource_settings(account_id,revision,updated_at) VALUES(?,1,?) ON CONFLICT(account_id) DO UPDATE SET revision=revision+1`, actor, s.timestamp()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_resource_overrides(account_id,dimension,requested) VALUES(?,?,?) ON CONFLICT(account_id,dimension) DO UPDATE SET requested=excluded.requested`, actor, dimension, value); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1 WHERE id=?`, actor)
		return err
	}))
}
func TestConcurrentProjectQuotaHasOneWinnerAndReplayDoesNotCharge(t *testing.T) {
	s, scope, _ := fixture(t)
	fixtureResourceLimit(t, s, scope.Actor, ResourceProjects, 2)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"last-a", "last-b"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			_, err := s.CreateProject(ctx, scope.Actor, id, id)
			results <- err
		}(id)
	}
	group.Wait()
	close(results)
	accepted, denied := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrQuotaExceeded) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || denied != 1 {
		t.Fatal(accepted, denied)
	}
	usage, err := scalarUsage(ctx, s.reader, scope.Actor, ResourceProjects)
	must(t, err)
	if usage.Used != 2 {
		t.Fatal(usage)
	}
	fixtureResourceLimit(t, s, scope.Actor, ResourceProjects, 1)
	_, err = s.CreateProject(ctx, scope.Actor, scope.ProjectID, "Alpha")
	must(t, err)
	_, err = s.CreateProject(ctx, scope.Actor, "overage", "Overage")
	wantError(t, err, ErrQuotaExceeded)
	var quota *QuotaError
	if !errors.As(err, &quota) || quota.Dimension != ResourceProjects || quota.Used != 2 || quota.RequestedDelta != 1 || quota.Limit != 1 {
		t.Fatal(err)
	}
	p, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	lifecycle := &Lifecycle{VerifyRuntime: testfixture.VerifyRuntime, Store: s, Backend: fakeBackend(), MaxProjects: 1}
	drive(t, lifecycle, scope.ProjectID)
	usage, err = scalarUsage(ctx, s.reader, scope.Actor, ResourceProjects)
	must(t, err)
	if usage.Used != 1 {
		t.Fatal(usage)
	}
}
func TestConcurrentComputeQuotaReservesBeforeAnyEngineCall(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	lifecycle.MaxProjects = 2
	s := lifecycle.Store
	other := Scope{Actor: scope.Actor, ProjectID: "other-owned"}
	_, err := s.CreateProject(ctx, other.Actor, other.ProjectID, "Other")
	must(t, err)
	fixtureResourceLimit(t, s, scope.Actor, ResourceRunningProjects, 1)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, target := range []Scope{scope, other} {
		group.Add(1)
		go func(target Scope) {
			defer group.Done()
			results <- lifecycle.EnsureActive(ctx, target, testProfile().Bindings())
		}(target)
	}
	group.Wait()
	close(results)
	accepted, denied := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrQuotaExceeded) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || denied != 1 || len(backend.volumes) != 0 || len(backend.creates) != 0 {
		t.Fatal(accepted, denied, backend.calls)
	}
	usage, err := computeUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage[ResourceRunningProjects].Reserved != 1 || usage[ResourceAggregateCPU].Reserved != 1000 {
		t.Fatal(usage)
	}
}
func TestFullPlanPersistentQuotaAndReductionPreserveWorkspace(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	s := lifecycle.Store
	profile := testProfile()
	profile.Volumes = append(profile.Volumes, VolumePlan{Key: "extra", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", CapacityBytes: 16 << 20})
	fixtureEnvironment(t, s, profile)
	fixtureResourceLimit(t, s, scope.Actor, ResourcePersistent, 112<<20)
	local := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	files := &Files{Store: s, Lifecycle: lifecycle, Backend: local, Bindings: profile.Bindings()}
	defer files.Close()
	_, err := files.Upload(ctx, scope, "initial", writeRequest("retained", "original"), strings.NewReader("original"))
	must(t, err)
	usage, err := storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 64<<20 || usage.Reserved != 48<<20 {
		t.Fatal(usage)
	}
	other := Scope{Actor: scope.Actor, ProjectID: "second"}
	_, err = s.CreateProject(ctx, other.Actor, other.ProjectID, "Second")
	must(t, err)
	_, err = files.Upload(ctx, other, "cannot-bind", writeRequest("new", "bytes"), strings.NewReader("bytes"))
	wantError(t, err, ErrQuotaExceeded)
	_, err = s.Resources(ctx, other)
	wantError(t, err, ErrNotFound)
	fixtureResourceLimit(t, s, scope.Actor, ResourcePersistent, 64<<20)
	wantError(t, lifecycle.Resume(ctx, scope, profile.Bindings()), ErrQuotaExceeded)
	_, err = files.Edit(ctx, scope, "edit-within-existing", writeRequest("retained", "preserved"), strings.NewReader("preserved"))
	must(t, err)
	actual, err := os.ReadFile(filepath.Join(local.root, "retained"))
	must(t, err)
	if string(actual) != "preserved" {
		t.Fatal(string(actual))
	}
	if len(backend.creates) != 0 {
		t.Fatal("quota denial started compute")
	}
	project, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, project.Revision))
	drive(t, lifecycle, scope.ProjectID)
	usage, err = storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) {
		t.Fatal(usage)
	}
}
func TestPendingComputeRechecksQuotaWithoutPretendingRemoteUncertainty(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	s := lifecycle.Store
	must(t, lifecycle.EnsureActive(ctx, scope, testProfile().Bindings()))
	// Prepare all storage and advance to the not-yet-dispatched sandbox phase.
	for i := 0; i < 3; i++ {
		must(t, lifecycle.Reconcile(ctx, scope.ProjectID))
	}
	fixtureResourceLimit(t, s, scope.Actor, ResourceCPU, 500)
	wantError(t, lifecycle.Reconcile(ctx, scope.ProjectID), ErrQuotaExceeded)
	if len(backend.creates) != 0 {
		t.Fatal("new compute ignored reduced allowance")
	}
	operation, err := pendingOperation(ctx, s.reader, scope.ProjectID)
	must(t, err)
	if operation.State != "pending" || !strings.Contains(operation.Error, "quota_exceeded") {
		t.Fatal(operation)
	}
	fixtureResourceLimit(t, s, scope.Actor, ResourceCPU, 1000)
	drive(t, lifecycle, scope.ProjectID)
	p, err := s.Project(ctx, scope)
	must(t, err)
	if p.State != "active" {
		t.Fatal(p)
	}
	fixtureResourceLimit(t, s, scope.Actor, ResourceCPU, 0)
	must(t, lifecycle.EnsureActive(ctx, scope, testProfile().Bindings()))
	must(t, lifecycle.Pause(ctx, scope, p.Revision))
	drive(t, lifecycle, scope.ProjectID)
	wantError(t, lifecycle.Resume(ctx, scope, testProfile().Bindings()), ErrQuotaExceeded)
}
func TestExecutionAndScheduleQuotasCountStartupIntent(t *testing.T) {
	lifecycle, scope, _ := lifecycleFixture(t)
	s := lifecycle.Store
	must(t, lifecycle.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, lifecycle, scope.ProjectID)
	fixtureResourceLimit(t, s, scope.Actor, ResourceAgents, 1)
	fixtureResourceLimit(t, s, scope.Actor, ResourceTerminals, 1)
	fixtureResourceLimit(t, s, scope.Actor, ResourceSchedules, 1)
	for index := 0; index < 2; index++ {
		_, err := s.CreateSession(ctx, scope, fmt.Sprintf("agent-%d", index), "Agent")
		must(t, err)
		_, err = s.CreateTerminal(ctx, scope, fmt.Sprintf("terminal-%d", index), "Terminal", 24, 80)
		must(t, err)
	}
	_, err := s.reserveAgent(ctx, scope, "agent-0")
	must(t, err)
	_, err = s.reserveAgent(ctx, scope, "agent-0")
	must(t, err)
	_, err = s.reserveAgent(ctx, scope, "agent-1")
	wantError(t, err, ErrQuotaExceeded)
	_, err = s.reserveTerminal(ctx, scope, "terminal-0", 24, 80)
	must(t, err)
	_, err = s.reserveTerminal(ctx, scope, "terminal-0", 24, 80)
	must(t, err)
	_, err = s.reserveTerminal(ctx, scope, "terminal-1", 24, 80)
	wantError(t, err, ErrQuotaExceeded)
	schedule := Schedule{ID: "one", Title: "One", Prompt: "work", TriggerKind: "manual", Timezone: "UTC", State: "active"}
	first, err := s.CreateSchedule(ctx, scope, schedule)
	must(t, err)
	schedule.ID = "two"
	_, err = s.CreateSchedule(ctx, scope, schedule)
	wantError(t, err, ErrQuotaExceeded)
	paused := "paused"
	_, err = s.MutateSchedule(ctx, scope, ScheduleMutation{ID: first.ID, RequestID: "pause", Operation: "update", Revision: first.Revision, Patch: SchedulePatch{State: &paused}})
	must(t, err)
	_, err = s.CreateSchedule(ctx, scope, schedule)
	must(t, err)
}
func TestTransferQuotaCoversActivePausedAndCachedReads(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprint(paused), func(t *testing.T) {
			files, scope, backend := fileFixture(t)
			s := files.Store
			if paused {
				p, err := s.Project(ctx, scope)
				must(t, err)
				must(t, files.Lifecycle.Pause(ctx, scope, p.Revision))
				drive(t, files.Lifecycle, scope.ProjectID)
			}
			must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("original"), 0600))
			snapshot, err := files.PreviewSnapshots().Open(ctx, scope, "file", 100)
			must(t, err)
			fixtureResourceLimit(t, s, scope.Actor, ResourceTransfer, 3)
			_, err = files.Upload(ctx, scope, "upload", writeRequest("file", "larger"), strings.NewReader("larger"))
			wantError(t, err, ErrQuotaExceeded)
			_, err = files.Capture(ctx, scope, "file")
			wantError(t, err, ErrQuotaExceeded)
			_, _, _, err = files.PreviewSnapshots().Acquire(ctx, scope, snapshot.ID)
			wantError(t, err, ErrQuotaExceeded)
			must(t, files.PreviewSnapshots().Release(ctx, scope, snapshot.ID))
			_, err = files.Edit(ctx, scope, "small", writeRequest("file", "yes"), strings.NewReader("yes"))
			must(t, err)
			actual, err := os.ReadFile(filepath.Join(backend.root, "file"))
			must(t, err)
			if string(actual) != "yes" {
				t.Fatal(string(actual))
			}
		})
	}
}

func TestConcurrentFirstUploadsCompeteForWholePersistentAllowance(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	s := lifecycle.Store
	other := Scope{Actor: scope.Actor, ProjectID: "other-upload"}
	_, err := s.CreateProject(ctx, other.Actor, other.ProjectID, "Other")
	must(t, err)
	fixtureResourceLimit(t, s, scope.Actor, ResourcePersistent, 96<<20)
	files := &Files{Store: s, Lifecycle: lifecycle, Backend: &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}, Bindings: testProfile().Bindings()}
	defer files.Close()
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, target := range []Scope{scope, other} {
		group.Add(1)
		go func(target Scope) {
			defer group.Done()
			_, err := files.Upload(ctx, target, "upload-"+target.ProjectID, writeRequest(target.ProjectID, "bytes"), strings.NewReader("bytes"))
			results <- err
		}(target)
	}
	group.Wait()
	close(results)
	accepted, denied := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrQuotaExceeded) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || denied != 1 || len(backend.volumes) != 1 || len(backend.creates) != 0 {
		t.Fatal(accepted, denied, backend.calls)
	}
	usage, err := storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 64<<20 || usage.Reserved != 32<<20 {
		t.Fatal(usage)
	}
}

type firstUploadBarrier struct {
	*fakeLifecycle
	started, release chan struct{}
	once             sync.Once
}

func (b *firstUploadBarrier) CreateVolume(ctx context.Context, key string, capacity int64) (sandbox.Volume, error) {
	if capacity == 64<<20 {
		b.once.Do(func() { close(b.started); <-b.release })
	}
	return b.fakeLifecycle.CreateVolume(ctx, key, capacity)
}
func TestConcurrentUploadAndComputeShareOneReservedPlan(t *testing.T) {
	lifecycle, scope, base := lifecycleFixture(t)
	s := lifecycle.Store
	backend := &firstUploadBarrier{fakeLifecycle: base, started: make(chan struct{}), release: make(chan struct{})}
	lifecycle.Backend = backend
	fixtureResourceLimit(t, s, scope.Actor, ResourcePersistent, 96<<20)
	local := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	files := &Files{Store: s, Lifecycle: lifecycle, Backend: local, Bindings: testProfile().Bindings()}
	defer files.Close()
	finished := make(chan error, 1)
	go func() {
		_, err := files.Upload(ctx, scope, "upload", writeRequest("retained", "bytes"), strings.NewReader("bytes"))
		finished <- err
	}()
	defer func() {
		select {
		case <-backend.release:
		default:
			close(backend.release)
		}
	}()
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach allocation barrier")
	}
	err := lifecycle.EnsureActive(ctx, scope, testProfile().Bindings())
	close(backend.release)
	must(t, err)
	wantError(t, <-finished, ErrPending)
	drive(t, lifecycle, scope.ProjectID)
	_, err = files.Upload(ctx, scope, "upload", writeRequest("retained", "bytes"), strings.NewReader("bytes"))
	must(t, err)
	usage, err := storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 96<<20 || usage.Reserved != 0 || len(base.volumes) != 2 || len(base.creates) != 1 {
		t.Fatal(usage, base.calls)
	}
	actual, err := os.ReadFile(filepath.Join(local.root, "retained"))
	must(t, err)
	if string(actual) != "bytes" {
		t.Fatal(string(actual))
	}
}
