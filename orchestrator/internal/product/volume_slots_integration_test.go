//go:build khoraintegration

package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/testfixture"
)

// This measures the public engine mechanisms used by slot initialization. It
// does not certify future registered release/path contracts or quota limits.
func TestProjectVolumeSlotsOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated PROJECT_TEST_ENDPOINT required")
	}
	runctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	api, err := sdk.New(endpoint)
	must(t, err)
	for i := 0; i < 100; i++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	staging, err := backend.CreateVolume(runctx, "slot-seed-staging", 8<<20)
	must(t, err)
	data := []byte("original seed\n")
	must(t, backend.WriteVolume(runctx, staging.VolumeReference, "sample", data))
	source, err := backend.SnapshotVolume(runctx, staging.VolumeReference, "slot-seed-publication", 8<<20)
	must(t, err)
	entries := []content.Entry{{Path: "sample", Size: int64(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data))}}
	frozen := &VolumeSource{ContentProfile:"reviewed-v1",Reference: ArtifactReference{ID: "examples", Version: "v1", Digest: content.Digest(entries)}, VolumeID: source.VolumeID, MinimumBytes: 8 << 20}
	store := testStore(t)
	must(t, store.write(runctx, func(tx *sql.Tx) error {
		return testfixture.RecordRuntimePublications(runctx, tx, []content.Publication{{StoreID: identity.StoreID, Purpose: "assets", RevisionDigest: frozen.Reference.Digest, Capacity: 8 << 20, Entries: entries, StagingVolumeID: staging.VolumeID, DerivedVolumeID: source.VolumeID, PublishedVolumeID: source.VolumeID}})
	}))
	scope := Scope{Actor: "owner", ProjectID: "slot-project"}
	_, err = store.CreateProject(runctx, scope.Actor, scope.ProjectID, "Slot qualification")
	must(t, err)
	lifecycle := &Lifecycle{VerifyRuntime: backend.VerifyRuntime, Store: store, Backend: backend, MaxProjects: 1}
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "agent-runtime"
	profile.MemoryBytes = 1 << 30
	profile.DiskBytes = 256 << 20
	profile.Volumes = append(profile.Volumes,
		VolumePlan{Key: "copy-a", Role: "data", Destination: "/data/a", Storage: "owned", Access: "read_write", CapacityBytes: 16 << 20, Source: frozen},
		VolumePlan{Key: "copy-b", Role: "data", Destination: "/data/b", Storage: "owned", Access: "read_only", CapacityBytes: 16 << 20, Source: frozen},
		VolumePlan{Key: "shared", Role: "data", Destination: "/datasets/reference", Storage: "shared_readonly", Access: "read_only", CapacityBytes: 8 << 20, Source: frozen})
	for index := 0; index < 1; index++ {
		profile.Volumes = append(profile.Volumes, VolumePlan{Key: fmt.Sprintf("extra-%d", index), Role: "data", Destination: fmt.Sprintf("/extra/%d", index), Storage: "owned", Access: "read_write", CapacityBytes: 8 << 20})
	}
	profile = fixtureRuntimeOnKhora(t, runctx, store, backend, profile, map[string][]content.Entry{frozen.Reference.Digest: entries})
	fixtureResourceLimit(t, store, scope.Actor, ResourcePersistent, 136<<20)
	files := &Files{Store: store, Lifecycle: lifecycle, Backend: backend, Bindings: profile.Bindings()}
	defer files.Close()
	_, err = files.Upload(runctx, scope, "before-compute", writeRequest("retained", "storage-only upload"), strings.NewReader("storage-only upload"))
	must(t, err)
	before, err := store.Resources(runctx, scope)
	must(t, err)
	beforeUsage, err := storageUsage(runctx, store.reader, scope.Actor)
	must(t, err)
	if before.Occupied || before.Reference.SandboxID != "" || beforeUsage.Allocated != 64<<20 || beforeUsage.Reserved != 72<<20 {
		t.Fatal("first upload did not reserve full plan without compute", before, beforeUsage)
	}
	account, err := store.Account(runctx, scope.Actor)
	must(t, err)
	_, token, err := store.Authenticate(runctx, account.Username, "fixture-password")
	must(t, err)
	projected, err := store.OwnUsage(runctx, token)
	must(t, err)
	if projected.Usage[ResourcePersistent] != (ResourceUsage{Used: 64 << 20, Reserved: 72 << 20}) || projected.Usage[ResourceRunningProjects] != (ResourceUsage{}) {
		t.Fatal(projected)
	}
	deniedScope := Scope{Actor: scope.Actor, ProjectID: "quota-denied-project"}
	_, err = store.CreateProject(runctx, deniedScope.Actor, deniedScope.ProjectID, "Over quota")
	must(t, err)
	_, err = files.Upload(runctx, deniedScope, "denied-upload", writeRequest("blocked", "bytes"), strings.NewReader("bytes"))
	wantError(t, err, ErrQuotaExceeded)
	_, err = store.Resources(runctx, deniedScope)
	wantError(t, err, ErrNotFound)
	must(t, lifecycle.EnsureActive(runctx, scope, profile.Bindings()))
	converge := func() {
		t.Helper()
		for n := 0; n < 40; n++ {
			err := lifecycle.Reconcile(runctx, scope.ProjectID)
			if errors.Is(err, ErrNotFound) {
				return
			}
			must(t, err)
		}
		t.Fatal("slot lifecycle did not converge")
	}
	converge()
	resource, err := lifecycle.ActiveResources(runctx, scope)
	must(t, err)
	if resource.VolumeForRole("workspace") != before.VolumeForRole("workspace") {
		t.Fatal("compute replaced first-upload workspace")
	}
	observed, err := backend.InspectConfiguration(runctx, resource.Reference)
	must(t, err)
	mounts, err := resource.Mounts()
	must(t, err)
	if len(observed.Mounts) != 8 || !sandbox.SameConfiguration(observed, profile.Reachability.Declaration(), mounts) {
		t.Fatal("mount projection differs", observed, mounts)
	}
	command := `set -eu
 test "$(cat /workspace/retained)" = 'storage-only upload'
 test "$(cat /data/a/sample)" = 'original seed'
 test "$(cat /data/b/sample)" = 'original seed'
 printf 'independent write' > /data/a/sample
 test "$(cat /data/b/sample)" = 'original seed'
 test "$(cat /datasets/reference/sample)" = 'original seed'
 if sh -c 'printf bad > /data/b/sample' 2>/dev/null; then exit 41; fi
 if sh -c 'printf bad > /datasets/reference/sample' 2>/dev/null; then exit 42; fi
 printf 'slot-copy-readonly-ok\n'`
	ref := sandbox.ProcessReference{StoreID: resource.Reference.StoreID, SandboxID: resource.Reference.SandboxID, Epoch: resource.Reference.Epoch, Handle: "slot-validation"}
	stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: ref.Handle, Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
	must(t, err)
	defer stream.Close()
	var output strings.Builder
	for {
		frame, err := stream.Next()
		must(t, err)
		if frame.Kind == "data" {
			output.Write(frame.Data)
		}
		if frame.Kind == "end" {
			if frame.ExitCode != 0 {
				t.Fatal(frame.ExitCode, output.String())
			}
			break
		}
	}
	if !strings.Contains(output.String(), "slot-copy-readonly-ok") {
		t.Fatal(output.String())
	}
	usage, err := storageUsage(runctx, store.reader, scope.Actor)
	must(t, err)
	if usage.Allocated != 136<<20 || usage.Reserved != 0 {
		t.Fatal("owned/shared accounting mismatch", usage)
	}
	projected, err = store.OwnUsage(runctx, token)
	must(t, err)
	if projected.Usage[ResourcePersistent] != (ResourceUsage{Used: 136 << 20}) || projected.Usage[ResourceRunningProjects].Used != 1 {
		t.Fatal(projected)
	}
	fixtureResourceLimit(t, store, scope.Actor, ResourcePersistent, 128<<20)
	fixtureResourceLimit(t, store, scope.Actor, ResourceMounts, 7)
	projected, err = store.OwnUsage(runctx, token)
	must(t, err)
	if len(projected.Overages) != 1 || projected.Overages[0].Dimension != ResourcePersistent || projected.SavedPlans.Count != 1 {
		t.Fatal(projected)
	}
	active, err := store.Project(runctx, scope)
	must(t, err)
	must(t, lifecycle.Pause(runctx, scope, active.Revision))
	converge()
	wantError(t, lifecycle.EnsureActive(runctx, scope, profile.Bindings()), ErrQuotaExceeded)
	_, err = files.Upload(runctx, scope, "after-reduction", writeRequest("retained", "still writable"), strings.NewReader("still writable"))
	must(t, err)
	project, err := store.Project(runctx, scope)
	must(t, err)
	must(t, store.DeleteProject(runctx, scope, project.Revision))
	converge()
	usage, err = storageUsage(runctx, store.reader, scope.Actor)
	must(t, err)
	if usage != (StorageUsage{}) {
		t.Fatal("cleanup retained storage charge", usage)
	}
	projected, err = store.OwnUsage(runctx, token)
	must(t, err)
	if projected.Usage[ResourcePersistent] != (ResourceUsage{}) || projected.SavedPlans.Count != 0 || len(projected.Overages) != 0 {
		t.Fatal(projected)
	}
	got, err := backend.ReadVolume(runctx, source.VolumeReference, "sample", 0, int64(len(data)))
	must(t, err)
	if string(got) != string(data) {
		t.Fatal("Project cleanup or copy write altered publication", string(got))
	}
	for _, slot := range resource.Volumes {
		plan, _ := resource.Profile.VolumeForRole(slot.Key)
		if slot.Key == "shared" || plan.Storage == "shared_readonly" {
			continue
		}
		_, err := api.Volume(runctx, slot.VolumeID)
		if err == nil {
			t.Fatal("owned volume retained after confirmed cleanup", slot.Key)
		}
	}
	must(t, backend.DeleteVolume(runctx, source.VolumeReference))
	must(t, backend.DeleteVolume(runctx, staging.VolumeReference))
	t.Logf("first upload: 64MiB allocated + 72MiB reserved, no compute; second full plan denied at 136MiB quota; observed %d mounts; independent native copies, read-only owned/shared mounts, 136MiB owned charge; own usage projects reservations and overage, reduced quotas block resume while paused upload works, zero after cleanup", len(mounts))
}
