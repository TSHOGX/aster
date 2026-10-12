package product

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

func TestFirstUploadReservesWholeSlotPlanAndResumeDoesNotDoubleCharge(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	profile := testProfile()
	profile.Volumes = append(profile.Volumes, VolumePlan{Key: "extra", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", CapacityBytes: 16 << 20})
	fixtureEnvironment(t, lifecycle.Store, profile)
	filesBackend := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	files := &Files{Store: lifecycle.Store, Lifecycle: lifecycle, Backend: filesBackend, Bindings: profile.Bindings()}
	defer files.Close()
	_, err := files.Upload(ctx, scope, "first-upload", writeRequest("retained", "user bytes"), strings.NewReader("user bytes"))
	must(t, err)
	usage, err := storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 64<<20 || usage.Reserved != 48<<20 {
		t.Fatal("upload did not reserve full plan", usage)
	}
	r, err := lifecycle.Store.Resources(ctx, scope)
	must(t, err)
	if r.Occupied || r.Reference.SandboxID != "" || len(r.Volumes) != 3 || r.VolumeForRole("runtime") != "" {
		t.Fatal(r)
	}
	originalWorkspace := r.VolumeForRole("workspace")
	reversed := profile
	slices.Reverse(reversed.Volumes)
	must(t, lifecycle.EnsureActive(ctx, scope, reversed.Bindings()))
	drive(t, lifecycle, scope.ProjectID)
	r, err = lifecycle.Store.Resources(ctx, scope)
	must(t, err)
	mounts, err := r.Mounts()
	must(t, err)
	if len(mounts) != 3 || r.VolumeForRole("workspace") != originalWorkspace || len(backend.volumes) != 3 {
		t.Fatal(r, mounts, backend.volumes)
	}
	usage, err = storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 112<<20 || usage.Reserved != 0 {
		t.Fatal("materialization double-charged", usage)
	}
	data, err := os.ReadFile(filepath.Join(filesBackend.root, "retained"))
	must(t, err)
	if string(data) != "user bytes" {
		t.Fatal(string(data))
	}
	project, err := lifecycle.Store.Project(ctx, scope)
	must(t, err)
	must(t, lifecycle.Store.DeleteProject(ctx, scope, project.Revision))
	drive(t, lifecycle, scope.ProjectID)
	usage, err = storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) || len(backend.volumes) != 0 {
		t.Fatal("cleanup retained charge", usage, backend.volumes)
	}
}

func TestUndispatchedSlotsReleaseWithoutAllocationAndLostCreatesRemainCharged(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	must(t, lifecycle.EnsureActive(ctx, scope, testProfile().Bindings()))
	backend.fail["volume"] = 1
	if err := lifecycle.Reconcile(ctx, scope.ProjectID); err == nil {
		t.Fatal("missing injected lost reply")
	}
	usage, err := storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage.Reserved != 96<<20 || usage.Allocated != 0 || len(backend.volumes) != 1 {
		t.Fatal(usage, backend.volumes)
	}
	// Simulate owner loss after the original remote effect. Slot keys and charge
	// survive, and retry binds the same single resource before materializing siblings.
	var number int
	var name, path string
	must(t, lifecycle.Store.reader.QueryRow(`PRAGMA database_list`).Scan(&number, &name, &path))
	must(t, lifecycle.Store.Close())
	reopened, err := openFixtureStore(ctx, path)
	must(t, err)
	defer reopened.Close()
	lifecycle.Store = reopened
	must(t, lifecycle.Reconcile(ctx, scope.ProjectID))
	if len(backend.volumes) != 1 {
		t.Fatal("lost create made duplicate volume", backend.volumes)
	}
	usage, err = storageUsage(ctx, reopened.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 32<<20 || usage.Reserved != 64<<20 {
		t.Fatal(usage)
	}
	// Deleting a still-pending create must abandon never-dispatched sibling slots,
	// rather than completing provisioning just to destroy it.
	project, err := reopened.Project(ctx, scope)
	must(t, err)
	must(t, reopened.DeleteProject(ctx, scope, project.Revision))
	for i := 0; i < 20; i++ {
		_ = lifecycle.Reconcile(ctx, scope.ProjectID)
	}
	usage, err = storageUsage(ctx, reopened.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) || backend.calls["volume"] != 2 || len(backend.creates) != 0 {
		t.Fatal(usage, backend.calls, backend.creates)
	}
}

type seededLifecycle struct {
	*fakeLifecycle
	trees              map[string]map[string][]byte
	refuseVerification bool
	reads              int
}

func (b *seededLifecycle) SnapshotVolume(ctx context.Context, source sandbox.VolumeReference, key string, capacity int64) (sandbox.Volume, error) {
	volume, err := b.fakeLifecycle.CreateVolume(ctx, key, capacity)
	if volume.VolumeID != "" && b.trees[volume.VolumeID] == nil {
		b.trees[volume.VolumeID] = map[string][]byte{}
		for name, data := range b.trees[source.VolumeID] {
			b.trees[volume.VolumeID][name] = slices.Clone(data)
		}
	}
	return volume, err
}
func (b *seededLifecycle) ListVolume(_ context.Context, ref sandbox.VolumeReference, _ string) ([]sandbox.VolumeEntry, error) {
	if b.refuseVerification {
		return nil, errors.New("injected verification outage")
	}
	entries := []sandbox.VolumeEntry{}
	for name, data := range b.trees[ref.VolumeID] {
		entries = append(entries, sandbox.VolumeEntry{Name: name, Type: "file", Size: int64(len(data))})
	}
	return entries, nil
}
func (b *seededLifecycle) LstatVolume(context.Context, sandbox.VolumeReference, string) (sandbox.VolumeEntry, error) {
	return sandbox.VolumeEntry{}, ErrInvalid
}
func (b *seededLifecycle) ReadVolume(_ context.Context, ref sandbox.VolumeReference, name string, offset, limit int64) ([]byte, error) {
	b.reads++
	data := b.trees[ref.VolumeID][name]
	return slices.Clone(data[offset:min(int64(len(data)), offset+limit)]), nil
}

func TestSeededSlotReadinessSeparatesAllocationAndPreservesUserChanges(t *testing.T) {
	lifecycle, scope, base := lifecycleFixture(t)
	seed := []byte("immutable seed")
	entries := []content.Entry{{Path: "sample", Size: int64(len(seed)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(seed))}}
	source := &VolumeSource{ContentProfile: "reviewed-v1", Reference: ArtifactReference{ID: "dataset", Version: "v1", Digest: content.Digest(entries)}, VolumeID: "published", MinimumBytes: 1 << 20}
	base.volumes["published"] = sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: "engine", VolumeID: "published"}, Capacity: 1 << 20}
	backend := &seededLifecycle{fakeLifecycle: base, trees: map[string]map[string][]byte{"published": {"sample": seed}}, refuseVerification: true}
	lifecycle.Backend = backend
	profile := testProfile()
	profile.Volumes = append(profile.Volumes, VolumePlan{Key: "copy", Role: "data", Destination: "/data", Storage: "owned", Access: "read_only", CapacityBytes: 4 << 20, Source: source}, VolumePlan{Key: "shared", Role: "data", Destination: "/reference", Storage: "shared_readonly", Access: "read_only", CapacityBytes: 1 << 20, Source: source})
	fixtureEnvironment(t, lifecycle.Store, profile, map[string][]content.Entry{source.Reference.Digest: entries})
	must(t, lifecycle.EnsureActive(ctx, scope, profile.Bindings()))
	if err := lifecycle.Reconcile(ctx, scope.ProjectID); err == nil {
		t.Fatal("verification outage did not block readiness")
	}
	r, err := lifecycle.Store.Resources(ctx, scope)
	must(t, err)
	_, err = r.Mounts()
	wantError(t, err, ErrPending)
	usage, err := storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 4<<20 || usage.Reserved != 96<<20 {
		t.Fatal("unready copy double-charged", usage)
	}
	backend.refuseVerification = false
	_, err = lifecycle.Store.db.Exec(`CREATE TRIGGER slot_ready_fault BEFORE UPDATE OF state ON project_volume_slots WHEN NEW.slot_key='copy' AND NEW.state='ready' BEGIN SELECT RAISE(ABORT,'lost readiness commit'); END`)
	must(t, err)
	if err := lifecycle.Reconcile(ctx, scope.ProjectID); err == nil {
		t.Fatal("readiness commit fault ignored")
	}
	if backend.reads != 1 {
		t.Fatal("copy was not verified before commit", backend.reads)
	}
	unready, err := lifecycle.Store.Resources(ctx, scope)
	must(t, err)
	if unready.Volumes[0].State != "initializing" || unready.Volumes[0].Error == "" || unready.Volumes[0].VolumeID != r.Volumes[0].VolumeID {
		t.Fatal("readiness failure replaced original copy", unready.Volumes)
	}
	_, err = lifecycle.Store.db.Exec(`DROP TRIGGER slot_ready_fault`)
	must(t, err)
	drive(t, lifecycle, scope.ProjectID)
	if backend.reads != 2 {
		t.Fatal("lost readiness commit did not reverify same isolated copy", backend.reads)
	}
	r, err = lifecycle.Store.Resources(ctx, scope)
	must(t, err)
	var copied string
	for _, slot := range r.Volumes {
		if slot.Key == "copy" {
			copied = slot.VolumeID
		}
	}
	if copied == "" || copied == source.VolumeID || string(backend.trees[copied]["sample"]) != string(seed) {
		t.Fatal(copied)
	}
	backend.trees[copied]["sample"] = []byte("user modification")
	reads := backend.reads
	must(t, lifecycle.EnsureActive(ctx, scope, profile.Bindings()))
	if _, err = lifecycle.materializeSlot(ctx, r, "copy"); err != nil {
		t.Fatal(err)
	}
	if backend.reads != reads || string(backend.trees[copied]["sample"]) != "user modification" || string(backend.trees["published"]["sample"]) != string(seed) {
		t.Fatal("ready data was reverified/reseeded")
	}
	project, err := lifecycle.Store.Project(ctx, scope)
	must(t, err)
	must(t, lifecycle.Store.DeleteProject(ctx, scope, project.Revision))
	drive(t, lifecycle, scope.ProjectID)
	if _, ok := backend.volumes["published"]; !ok {
		t.Fatal("project cleanup deleted shared publication")
	}
	usage, err = storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) {
		t.Fatal(usage)
	}
}

func TestUncertainStorageOnlySlotBlocksReleaseReadiness(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	files := &Files{Store: lifecycle.Store, Lifecycle: lifecycle, Backend: &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}, Bindings: testProfile().Bindings()}
	defer files.Close()
	backend.fail["volume"] = 1
	_, err := files.Upload(ctx, scope, "upload", writeRequest("file", "bytes"), strings.NewReader("bytes"))
	if err == nil {
		t.Fatal("lost create not injected")
	}
	drain, err := lifecycle.Store.BeginReleaseDrain(ctx, "storage-drain", time.Minute)
	must(t, err)
	status, err := lifecycle.Store.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.DurableBlockers["volume_slot"] != 1 {
		t.Fatal("storage-only uncertainty missing from release barrier", status.DurableBlockers)
	}
	_, err = lifecycle.Store.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrBusy)
	project, err := lifecycle.Store.Project(ctx, scope)
	must(t, err)
	must(t, lifecycle.Store.DeleteProject(ctx, scope, project.Revision))
	drive(t, lifecycle, scope.ProjectID)
	usage, err := storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) || len(backend.creates) != 0 {
		t.Fatal(usage, backend.creates)
	}
	_, err = lifecycle.Store.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
}

func TestCleanupReleasesObservedAbsentOriginalVolume(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	files := &Files{Store: lifecycle.Store, Lifecycle: lifecycle, Backend: &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}, Bindings: testProfile().Bindings()}
	defer files.Close()
	backend.fail["volume"] = 1
	_, err := files.Upload(ctx, scope, "upload", writeRequest("file", "bytes"), strings.NewReader("bytes"))
	if err == nil {
		t.Fatal("expected lost allocation reply")
	}
	for id := range backend.volumes {
		delete(backend.volumes, id)
	}
	project, err := lifecycle.Store.Project(ctx, scope)
	must(t, err)
	must(t, lifecycle.Store.DeleteProject(ctx, scope, project.Revision))
	drive(t, lifecycle, scope.ProjectID)
	usage, err := storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) {
		t.Fatal("observed absent volume leaked reservation", usage)
	}
}

func TestLostSlotDeletionRetainsChargeUntilConfirmedAbsence(t *testing.T) {
	lifecycle, scope, backend := lifecycleFixture(t)
	must(t, lifecycle.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, lifecycle, scope.ProjectID)
	project, err := lifecycle.Store.Project(ctx, scope)
	must(t, err)
	must(t, lifecycle.Store.DeleteProject(ctx, scope, project.Revision))
	must(t, lifecycle.Reconcile(ctx, scope.ProjectID))
	backend.fail["delete-volume"] = 1
	if err := lifecycle.Reconcile(ctx, scope.ProjectID); err == nil {
		t.Fatal("lost delete not injected")
	}
	usage, err := storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 96<<20 || len(backend.volumes) != 1 {
		t.Fatal("uncertain delete freed charge early", usage, len(backend.volumes))
	}
	resources, err := lifecycle.Store.Resources(ctx, scope)
	must(t, err)
	if resources.Volumes[0].State != "deleting" {
		t.Fatal(resources.Volumes)
	}
	drive(t, lifecycle, scope.ProjectID)
	usage, err = storageUsage(ctx, lifecycle.Store.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) || len(backend.volumes) != 0 {
		t.Fatal(usage, backend.volumes)
	}
}

func TestUnreadyWorkspaceSeedIsNotAnEmptyReadableDirectory(t *testing.T) {
	lifecycle, scope, base := lifecycleFixture(t)
	data := []byte("seed contents")
	entries := []content.Entry{{Path: "sample", Size: int64(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data))}}
	backend := &seededLifecycle{fakeLifecycle: base, trees: map[string]map[string][]byte{"source": {"sample": data}}, refuseVerification: true}
	lifecycle.Backend = backend
	profile := testProfile()
	for index := range profile.Volumes {
		if profile.Volumes[index].Role == "workspace" {
			profile.Volumes[index].Source = &VolumeSource{ContentProfile: "reviewed-v1", Reference: ArtifactReference{ID: "workspace-seed", Version: "v1", Digest: content.Digest(entries)}, VolumeID: "source", MinimumBytes: 1 << 20}
		}
	}
	filesBackend := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	fixtureEnvironment(t, lifecycle.Store, profile, map[string][]content.Entry{content.Digest(entries): entries})
	files := &Files{Store: lifecycle.Store, Lifecycle: lifecycle, Backend: filesBackend, Bindings: profile.Bindings()}
	defer files.Close()
	_, err := files.Upload(ctx, scope, "upload", writeRequest("new", "new bytes"), strings.NewReader("new bytes"))
	if err == nil {
		t.Fatal("unverified seed exposed to upload")
	}
	_, err = files.List(ctx, scope, projectfiles.DirectoryQuery{Limit: 10})
	wantError(t, err, ErrPending)
	_, err = files.Capture(ctx, scope, "sample")
	wantError(t, err, ErrPending)
	if filesBackend.starts != 0 {
		t.Fatal("unready seed started a guest helper")
	}
}
