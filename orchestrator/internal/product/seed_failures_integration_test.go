//go:build khoraintegration

package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/testfixture"
)

// Faults discard actual engine replies or temporarily deny verification; all
// underlying resource creation/copy/read/delete operations use public Khora APIs.
type seedFaultBackend struct {
	*khora.Backend
	lostSnapshot, blockVerification bool
	snapshots, deletes              int
	original                        sandbox.Volume
}

func (b *seedFaultBackend) SnapshotVolume(ctx context.Context, source sandbox.VolumeReference, key string, capacity int64) (sandbox.Volume, error) {
	b.snapshots++
	volume, err := b.Backend.SnapshotVolume(ctx, source, key, capacity)
	if err != nil {
		return volume, err
	}
	if b.original.VolumeID != "" && b.original.VolumeID != volume.VolumeID {
		return volume, fmt.Errorf("snapshot retry substituted original identity")
	}
	b.original = volume
	if b.lostSnapshot {
		b.lostSnapshot = false
		return sandbox.Volume{}, &sandbox.Error{Class: sandbox.Uncertain, Operation: "snapshot", Cause: fmt.Errorf("discarded actual successful reply")}
	}
	return volume, nil
}
func (b *seedFaultBackend) ListVolume(ctx context.Context, ref sandbox.VolumeReference, path string) ([]sandbox.VolumeEntry, error) {
	if b.blockVerification && ref.VolumeID == b.original.VolumeID {
		return nil, &sandbox.Error{Class: sandbox.Unavailable, Operation: "verify seed", Cause: fmt.Errorf("verification transport unavailable")}
	}
	return b.Backend.ListVolume(ctx, ref, path)
}
func (b *seedFaultBackend) DeleteVolume(ctx context.Context, ref sandbox.VolumeReference) error {
	b.deletes++
	return b.Backend.DeleteVolume(ctx, ref)
}

func TestProjectSeedFailuresOnKhora(t *testing.T) {
	runctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	api, err := sdk.New(endpoint)
	must(t, err)
	for attempt := 0; attempt < 100; attempt++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	staging, err := backend.CreateVolume(runctx, "fault-seed-staging", 8<<20)
	must(t, err)
	seed := []byte("original verified seed\n")
	must(t, backend.WriteVolume(runctx, staging.VolumeReference, "sample", seed))
	source, err := backend.SnapshotVolume(runctx, staging.VolumeReference, "fault-seed-published", 8<<20)
	must(t, err)
	entries := []content.Entry{{Path: "sample", Size: int64(len(seed)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(seed))}}
	reference := ArtifactReference{ID: "workspace-seed", Version: "v1", Digest: content.Digest(entries)}
	s := testStore(t)
	must(t, s.write(runctx, func(tx *sql.Tx) error {
		return testfixture.RecordRuntimePublications(runctx, tx, []content.Publication{{StoreID: identity.StoreID, Purpose: "assets", RevisionDigest: reference.Digest, Capacity: 8 << 20, Entries: entries, StagingVolumeID: staging.VolumeID, DerivedVolumeID: source.VolumeID, PublishedVolumeID: source.VolumeID}})
	}))
	scope := Scope{Actor: "owner", ProjectID: "seed-faults"}
	_, err = s.CreateProject(runctx, scope.Actor, scope.ProjectID, "Actual seed fault qualification")
	must(t, err)
	profile := testProfile()
	profile.StoreID, profile.TemplateID = identity.StoreID, "agent-runtime"
	profile.MemoryBytes, profile.DiskBytes = 1<<30, 256<<20
	for index := range profile.Volumes {
		if profile.Volumes[index].Role == "workspace" {
			profile.Volumes[index].Key = "a-workspace"
			profile.Volumes[index].Source = &VolumeSource{ContentProfile:"reviewed-v1",Reference: reference, VolumeID: source.VolumeID, MinimumBytes: 8 << 20}
		}
	}
	profile = fixtureRuntimeOnKhora(t, runctx, s, backend, profile, map[string][]content.Entry{reference.Digest: entries})
	faults := &seedFaultBackend{Backend: backend, lostSnapshot: true, blockVerification: true}
	l := &Lifecycle{Store: s, Backend: faults, MaxProjects: 1, VerifyRuntime: backend.VerifyRuntime}
	must(t, l.EnsureActive(runctx, scope, profile.Bindings()))
	if err = l.Reconcile(runctx, scope.ProjectID); err == nil {
		t.Fatal("lost actual clone reply ignored")
	}
	first, err := s.Resources(runctx, scope)
	must(t, err)
	usage, err := storageUsage(runctx, s.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{Reserved: 96 << 20}) || faults.original.VolumeID == "" {
		t.Fatal(usage, faults.original)
	}
	// Restart the writer with the same durable slot intent and actual remote clone.
	var number int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&number, &name, &path))
	must(t, s.Close())
	s, err = openFixtureStore(runctx, path)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	l.Store = s
	if err = l.Reconcile(runctx, scope.ProjectID); err == nil {
		t.Fatal("verification outage admitted workspace")
	}
	unready, err := s.Resources(runctx, scope)
	must(t, err)
	usage, err = storageUsage(runctx, s.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{Allocated: 64 << 20, Reserved: 32 << 20}) || unready.Volumes[0].VolumeID != faults.original.VolumeID || unready.VolumeForRole("workspace") != "" || first.Profile.EnvironmentRevision != unready.Profile.EnvironmentRevision {
		t.Fatal(usage, unready)
	}
	files := &Files{Store: s, Lifecycle: l, Backend: backend, Bindings: profile.Bindings()}
	defer files.Close()
	_, err = files.List(runctx, scope, projectfiles.DirectoryQuery{Limit: 10})
	wantError(t, err, ErrPending)
	_, err = files.Capture(runctx, scope, "sample")
	wantError(t, err, ErrPending)
	faults.blockVerification = false
	_, err = s.db.Exec(`CREATE TRIGGER seed_ready_fault BEFORE UPDATE OF state ON project_volume_slots WHEN NEW.slot_key='a-workspace' AND NEW.state='ready' BEGIN SELECT RAISE(ABORT,'lost seed readiness commit'); END`)
	must(t, err)
	if err = l.Reconcile(runctx, scope.ProjectID); err == nil {
		t.Fatal("readiness commit fault ignored")
	}
	_, err = files.List(runctx, scope, projectfiles.DirectoryQuery{Limit: 10})
	wantError(t, err, ErrPending)
	_, err = s.db.Exec(`DROP TRIGGER seed_ready_fault`)
	must(t, err)
	adminToken := suspensionAdmin(t, s)
	owner, err := s.Account(runctx, scope.Actor)
	must(t, err)
	suspended, err := s.SuspendAccount(runctx, adminToken, scope.Actor, AccountRevision{RequestID: "suspend-unready-seed", ExpectedRevision: owner.Revision})
	must(t, err)
	worker := &Workers{Store: s, Lifecycle: l}
	p, err := s.OperatorProject(runctx, scope.ProjectID)
	must(t, err)
	calls := faults.snapshots
	must(t, worker.project(runctx, p))
	must(t, worker.suspensionOperations(runctx))
	operation, err := s.AdminOperation(runctx, adminToken, suspended.Operation.ID)
	must(t, err)
	if operation.State != "succeeded" || faults.snapshots != calls {
		t.Fatal(operation, faults.snapshots)
	}
	_, err = s.EnableAccount(runctx, adminToken, scope.Actor, AccountRevision{RequestID: "enable-unready-seed", ExpectedRevision: suspended.User.Revision})
	must(t, err)
	wantError(t, l.EnsureActive(runctx, scope, profile.Bindings()), ErrForbidden)
	must(t, l.Resume(runctx, scope, profile.Bindings()))
	drive(t, l, scope.ProjectID)
	ready, err := l.ActiveResources(runctx, scope)
	must(t, err)
	if ready.VolumeForRole("workspace") != faults.original.VolumeID || faults.snapshots != calls {
		t.Fatal("resume reseeded original clone", ready, faults.snapshots)
	}
	_, err = files.Upload(runctx, scope, "change-ready-copy", writeRequest("sample", "private retained edit"), strings.NewReader("private retained edit"))
	must(t, err)
	p, err = s.Project(runctx, scope)
	must(t, err)
	must(t, l.Pause(runctx, scope, p.Revision))
	drive(t, l, scope.ProjectID)
	must(t, l.Resume(runctx, scope, profile.Bindings()))
	drive(t, l, scope.ProjectID)
	copyBytes, err := backend.ReadVolume(runctx, faults.original.VolumeReference, "sample", 0, 64)
	must(t, err)
	if string(copyBytes) != "private retained edit" || faults.snapshots != calls {
		t.Fatal("ready data was reseeded", string(copyBytes))
	}
	p, err = s.Project(runctx, scope)
	must(t, err)
	must(t, s.DeleteProject(runctx, scope, p.Revision))
	drive(t, l, scope.ProjectID)
	usage, err = storageUsage(runctx, s.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) {
		t.Fatal("owned charges retained after cleanup", usage)
	}
	_, err = backend.InspectVolume(runctx, faults.original.VolumeReference)
	if !sandbox.Is(err, sandbox.NotFound) {
		t.Fatal("owned clone survived cleanup", err)
	}
	got, err := backend.ReadVolume(runctx, source.VolumeReference, "sample", 0, 64)
	must(t, err)
	if string(got) != string(seed) {
		t.Fatal("source changed", string(got))
	}
	must(t, backend.DeleteVolume(runctx, source.VolumeReference))
	must(t, backend.DeleteVolume(runctx, staging.VolumeReference))
	t.Log("actual lost native clone reply, writer replacement, unavailable verification, lost readiness commit, unexposed seed, suspend/enable/explicit resume, preserved ready edits and exact owned cleanup passed; native snapshot is atomic, no streaming seed transfer is used")
}
