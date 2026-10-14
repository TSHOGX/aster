package product

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/testfixture"
)

func TestFirstAllocationSelectsDatabaseEnvironmentAndUserRequests(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	cpu, memory, workspace, runtime, aggregate := int64(1500), int64(512<<20), int64(64<<20), int64(32<<20), int64(8000)
	applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: admin.ID, Overrides: map[ResourceDimension]*int64{ResourceCPU: &cpu, ResourceMemory: &memory, ResourceWorkspace: &workspace, ResourceRuntime: &runtime, ResourceAggregateCPU: &aggregate}})
	scope := Scope{Actor: admin.ID, ProjectID: "database-selected"}
	_, err := s.CreateProject(ctx, scope.Actor, scope.ProjectID, "Selected")
	must(t, err)
	backend := fakeBackend()
	lifecycle := &Lifecycle{Store: s, Backend: backend, MaxProjects: 3, VerifyRuntime: testfixture.VerifyRuntime}
	must(t, lifecycle.EnsureActive(ctx, scope, RuntimeBindings{StoreID: "engine"}))
	drive(t, lifecycle, scope.ProjectID)
	original, err := s.Resources(ctx, scope)
	must(t, err)
	if original.Profile.CPUMillis != cpu || original.Profile.MemoryBytes != memory || original.Profile.CapacityForRole("workspace") != workspace || original.Profile.EnvironmentRevision != 1 || original.Profile.ResourceRevision == 0 || original.Profile.SandboxRelease.Version != "v1" {
		t.Fatal(original.Profile)
	}
	release, err := s.RetainedRuntime(ctx, original.Profile)
	must(t, err)
	release.Reference.ID = "second-release"
	release.Reference.Version = "v2"
	release.TemplateID = "second-template"
	must(t, s.RegisterSandbox(ctx, release))
	// Registration alone does not select a new default or alter saved Profiles.
	selected, _, err := testEnvironment(ctx, s.reader)
	must(t, err)
	if selected.SandboxRelease == release.Reference {
		t.Fatal("registration selected a default")
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		selected.Revision++
		selected.SandboxRelease = release.Reference
		selected.Mounts = append(selected.Mounts, EnvironmentMount{Key: "data", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", Source: MountSource{Kind: "empty"}, Capacity: MountCapacity{Bytes: 16 << 20}})
		raw, err := json.Marshal(selected)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE environment_templates SET revision=?,document=? WHERE id='standard-agent'`, selected.Revision, string(raw))
		return err
	}))
	settings, err := s.UserResourceSettings(ctx, token, admin.ID)
	must(t, err)
	cpu, mounts := int64(1000), int64(3)
	applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: admin.ID, ExpectedRevision: settings.Revision, Overrides: map[ResourceDimension]*int64{ResourceCPU: &cpu, ResourceMounts: &mounts}})
	// Lowering the CPU request also caps resume of the old larger saved request;
	// it does not resize that snapshot. Restore headroom to test retention itself.
	fixtureResourceLimit(t, s, admin.ID, ResourceCPU, 2000)
	project, err := s.Project(ctx, scope)
	must(t, err)
	must(t, lifecycle.Pause(ctx, scope, project.Revision))
	drive(t, lifecycle, scope.ProjectID)
	must(t, lifecycle.Resume(ctx, scope, RuntimeBindings{StoreID: "engine"}))
	drive(t, lifecycle, scope.ProjectID)
	retained, err := s.Resources(ctx, scope)
	must(t, err)
	if retained.Profile.TemplateID != original.Profile.TemplateID || retained.Profile.CPUMillis != original.Profile.CPUMillis || len(retained.Profile.Volumes) != 2 || retained.VolumeForRole("workspace") != original.VolumeForRole("workspace") {
		t.Fatal("default/resource edit rebound existing Project", retained.Profile)
	}
	next := Scope{Actor: admin.ID, ProjectID: "second"}
	_, err = s.CreateProject(ctx, next.Actor, next.ProjectID, "Second")
	must(t, err)
	must(t, lifecycle.EnsureActive(ctx, next, RuntimeBindings{StoreID: "engine"}))
	current, err := s.Resources(ctx, next)
	must(t, err)
	if current.Profile.TemplateID != "second-template" || current.Profile.EnvironmentRevision != 2 || current.Profile.CPUMillis != 2000 || len(current.Profile.Volumes) != 3 {
		t.Fatal(current.Profile)
	}
	must(t, s.ValidateReleasedBindings(ctx, "engine"))
}
func TestDatabaseAllocationHasNoMissingRegistryOrDefaultFallback(t *testing.T) {
	for _, fault := range []string{"selection", "release", "disabled", "digest", "store"} {
		t.Run(fault, func(t *testing.T) {
			lifecycle, scope, backend := lifecycleFixture(t)
			s := lifecycle.Store
			switch fault {
			case "selection":
				_, err := s.db.Exec(`DELETE FROM project_configuration`)
				must(t, err)
			case "release":
				_, err := s.db.Exec(`DELETE FROM registered_sandboxes`)
				must(t, err)
			case "disabled":
				_, err := s.db.Exec(`UPDATE registered_sandboxes SET enabled=0`)
				must(t, err)
			case "digest":
				_, err := s.db.Exec(`UPDATE registered_sandboxes SET digest=?`, "sha256:"+strings.Repeat("1", 64))
				must(t, err)
			case "store":
				_, err := s.db.Exec(`UPDATE registered_sandboxes SET store_id='foreign'`)
				must(t, err)
			}
			err := lifecycle.EnsureActive(ctx, scope, testProfile().Bindings())
			if fault == "store" {
				wantError(t, err, ErrConflict)
			} else {
				if fault == "selection" {
					wantError(t, err, ErrNotFound)
				} else {
					wantError(t, err, ErrArtifactUnavailable)
				}
			}
			_, err = s.Resources(ctx, scope)
			wantError(t, err, ErrNotFound)
			if len(backend.volumes) != 0 || len(backend.creates) != 0 {
				t.Fatal("invalid default reached engine")
			}
		})
	}
}
func TestStorageFirstDatabaseBindingDoesNotReserveCompute(t *testing.T) {
	lifecycle, scope, _ := lifecycleFixture(t)
	s := lifecycle.Store
	fixtureResourceLimit(t, s, scope.Actor, ResourceRunningProjects, 0)
	fixtureResourceLimit(t, s, scope.Actor, ResourceAggregateCPU, 0)
	fixtureResourceLimit(t, s, scope.Actor, ResourceWorkspace, 8<<20)
	fixtureResourceLimit(t, s, scope.Actor, ResourceRuntime, 4<<20)
	files := &Files{Store: s, Lifecycle: lifecycle, Backend: &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}, Bindings: RuntimeBindings{StoreID: "engine"}}
	defer files.Close()
	_, err := files.Upload(ctx, scope, "first", writeRequest("file", "bytes"), strings.NewReader("bytes"))
	must(t, err)
	resource, err := s.Resources(ctx, scope)
	must(t, err)
	usage, err := storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if resource.Occupied || resource.Profile.CapacityForRole("workspace") != 8<<20 || usage.Allocated != 8<<20 || usage.Reserved != 4<<20 {
		t.Fatal(resource, usage)
	}
	err = lifecycle.Resume(ctx, scope, RuntimeBindings{StoreID: "engine"})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal(err)
	}
}

func TestFirstBindingRetainsSelectedEnvironmentAndReadsCurrentQuotas(t *testing.T) {
	lifecycle, scope, _ := lifecycleFixture(t)
	s := lifecycle.Store
	environment, _, err := testEnvironment(ctx, s.reader)
	must(t, err)
	environment.Revision++
	environment.Mounts = append(environment.Mounts, EnvironmentMount{Key: "late-data", Role: "data", Destination: "/late-data", Storage: "owned", Access: "read_write", Source: MountSource{Kind: "empty"}, Capacity: MountCapacity{Bytes: 16 << 20}})
	raw, err := json.Marshal(environment)
	must(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	writer := make(chan error, 1)
	go func() {
		writer <- s.write(ctx, func(tx *sql.Tx) error {
			close(entered)
			<-release
			if _, err := tx.ExecContext(ctx, `UPDATE environment_templates SET revision=?,document=? WHERE id='fixture-environment'`, environment.Revision, string(raw)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_resource_settings(account_id,revision,updated_at) VALUES(?,1,?)`, scope.Actor, s.timestamp()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_resource_overrides(account_id,dimension,requested) VALUES(?,'workspace_bytes',?)`, scope.Actor, 16<<20); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1 WHERE id=?`, scope.Actor)
			return err
		})
	}()
	<-entered
	bound := make(chan error, 1)
	go func() { bound <- lifecycle.EnsureActive(ctx, scope, RuntimeBindings{StoreID: "engine"}) }()
	close(release)
	must(t, <-writer)
	must(t, <-bound)
	snapshot, err := s.Resources(ctx, scope)
	must(t, err)
	if snapshot.Profile.EnvironmentRevision != environment.Revision-1 || snapshot.Profile.CapacityForRole("workspace") != 16<<20 || len(snapshot.Volumes) != 2 {
		t.Fatal(snapshot.Profile)
	}
	usage, err := storageUsage(ctx, s.reader, scope.Actor)
	must(t, err)
	if usage.Reserved != 48<<20 || usage.Allocated != 0 {
		t.Fatal(usage)
	}
}
