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

type lostPublicationReply struct {
	*khora.Backend
	lose    bool
	patches int
}

func (b *lostPublicationReply) UpdateConfiguration(ctx context.Context, ref sandbox.Reference, network sandbox.Network, mounts []sandbox.Mount) (sandbox.Configuration, error) {
	b.patches++
	out, err := b.Backend.UpdateConfiguration(ctx, ref, network, mounts)
	if err == nil && b.lose {
		b.lose = false
		return sandbox.Configuration{}, errors.New("qualification: response lost after successful configuration patch")
	}
	return out, err
}
func TestEnvironmentPublicationUpdatesOnKhora(t *testing.T) {
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
	store := testStore(t)
	token := suspensionAdmin(t, store)
	publications := []content.Publication{}
	references := []ArtifactReference{}
	for _, version := range []string{"v1", "v2"} {
		bytes := []byte("knowledge-" + version + "\n")
		entries := []content.Entry{{Path: "knowledge.txt", Size: int64(len(bytes)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(bytes))}}
		staged, e := backend.CreateVolume(runctx, "update-staging-"+version, 8<<20)
		must(t, e)
		must(t, backend.WriteVolume(runctx, staged.VolumeReference, "knowledge.txt", bytes))
		frozen, e := backend.SnapshotVolume(runctx, staged.VolumeReference, "update-frozen-"+version, 8<<20)
		must(t, e)
		p := content.Publication{StoreID: identity.StoreID, Purpose: "builtin", RevisionDigest: content.Digest(entries), Capacity: 8 << 20, Entries: entries, StagingVolumeID: staged.VolumeID, DerivedVolumeID: frozen.VolumeID, PublishedVolumeID: frozen.VolumeID}
		publications = append(publications, p)
		references = append(references, ArtifactReference{ID: "knowledge", Version: version, Digest: p.RevisionDigest})
	}
	must(t, store.write(runctx, func(tx *sql.Tx) error { return testfixture.RecordRuntimePublications(runctx, tx, publications) }))
	scope := Scope{Actor: "owner", ProjectID: "publication-update"}
	_, err = store.CreateProject(runctx, scope.Actor, scope.ProjectID, "Update qualification")
	must(t, err)
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "agent-runtime"
	profile.MemoryBytes = 1 << 30
	profile.DiskBytes = 256 << 20
	profile.Volumes = append(profile.Volumes, VolumePlan{Key: "data", Role: "data", Destination: "/data", Storage: "shared_readonly", Access: "read_only", CapacityBytes: 8 << 20, Source: &VolumeSource{ContentProfile: "reviewed-v1", Reference: references[0], VolumeID: publications[0].PublishedVolumeID, MinimumBytes: 8 << 20}})
	profile = fixtureRuntimeOnKhora(t, runctx, store, backend, profile, map[string][]content.Entry{references[0].Digest: publications[0].Entries})
	wrapper := &lostPublicationReply{Backend: backend}
	lifecycle := &Lifecycle{Store: store, Backend: wrapper, VerifyRuntime: backend.VerifyRuntime, MaxProjects: 2}
	converge := func() {
		t.Helper()
		for i := 0; i < 40; i++ {
			e := lifecycle.Reconcile(runctx, scope.ProjectID)
			if errors.Is(e, ErrNotFound) {
				return
			}
			must(t, e)
		}
		t.Fatal("update did not converge")
	}
	command := func(handle, script string) {
		t.Helper()
		r, e := lifecycle.ActiveResources(runctx, scope)
		must(t, e)
		ref := sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: handle}
		stream, e := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", script}, Directory: profile.RolePath("workspace")})
		must(t, e)
		defer stream.Close()
		var output strings.Builder
		for {
			frame, e := stream.Next()
			must(t, e)
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
	}
	must(t, lifecycle.EnsureActive(runctx, scope, profile.Bindings()))
	converge()
	before, err := store.Resources(runctx, scope)
	must(t, err)
	command("before-update", `set -eu; test "$(cat /data/knowledge.txt)" = knowledge-v1; printf retained > retained`)
	must(t, store.RegisterBuiltinVolume(runctx, BuiltinVolume{Reference: references[1], StoreID: identity.StoreID, PublicationPurpose: "builtin", MinimumBytes: 8 << 20, Roles: []string{"data"}, Entries: publications[1].Entries}))
	secondScope := Scope{Actor: "alice", ProjectID: "unallocated-publication-update"}
	_, err = store.CreateProject(runctx, secondScope.Actor, secondScope.ProjectID, "Another user's project")
	must(t, err)
	id := selectPublicationFixture(t, store, references[1], false)
	publishUpdateFixture(t, store, token, id)
	command("still-running", `test "$(cat /data/knowledge.txt)" = knowledge-v1`)
	project, err := store.Project(runctx, scope)
	must(t, err)
	must(t, lifecycle.Pause(runctx, scope, project.Revision))
	converge()
	must(t, lifecycle.Resume(runctx, scope, profile.Bindings()))
	wrapper.lose = true
	if lifecycle.Reconcile(runctx, scope.ProjectID) == nil {
		t.Fatal("lost response not injected")
	}
	converge()
	after, err := store.Resources(runctx, scope)
	must(t, err)
	if wrapper.patches != 1 || after.VolumeForRole("workspace") != before.VolumeForRole("workspace") || after.VolumeForRole("runtime") != before.VolumeForRole("runtime") {
		t.Fatal("duplicate patch or replaced owned volume")
	}
	command("after-update", `set -eu; test "$(cat /data/knowledge.txt)" = knowledge-v2; test "$(cat retained)" = retained; if sh -c 'printf bad > /data/knowledge.txt' 2>/dev/null; then exit 41; fi`)
	actual, err := backend.InspectConfiguration(runctx, after.Reference)
	must(t, err)
	if !profileConfiguration(actual, after.Profile, after) {
		t.Fatal("saved and observed mounts differ")
	}
	st, err := updateStatus(runctx, store.reader, scope.ProjectID)
	must(t, err)
	if st.State != "current" {
		t.Fatal(st)
	}
	must(t, store.ValidateReleasedBindings(runctx, identity.StoreID))
	// The same publication reaches a different owner's never-started Project.
	firstScope := scope
	scope = secondScope
	must(t, lifecycle.EnsureActive(runctx, scope, profile.Bindings()))
	converge()
	command("first-start-new-data", `set -eu; test "$(cat /data/knowledge.txt)" = knowledge-v2; test ! -e retained`)
	secondProject, e := store.Project(runctx, scope)
	must(t, e)
	must(t, store.DeleteProject(runctx, scope, secondProject.Revision))
	converge()
	scope = firstScope

	// Stop an uncertain, not-yet-observed update. The real engine must advance
	// the validator even for a no-op binding fence, and a lost fence response
	// must not cause a second patch or apply the stopped target.
	selectPublicationFixture(t, store, references[0], false)
	stoppedRelease := publishUpdateFixture(t, store, token, id)
	currentProject, e := store.Project(runctx, scope)
	must(t, e)
	must(t, lifecycle.Pause(runctx, scope, currentProject.Revision))
	converge()
	must(t, lifecycle.Resume(runctx, scope, profile.Bindings()))
	pending, e := pendingOperation(runctx, store.reader, scope.ProjectID)
	must(t, e)
	if lifecycle.unknown(runctx, pending, errors.New("qualification: outbound request delivery unknown")) == nil {
		t.Fatal("missing uncertainty")
	}
	controlUpdateFixture(t, store, token, id, EnvironmentUpdateEdit{Action: "stop", ReleaseID: stoppedRelease.ID, ExpectedRevision: 1})
	wrapper.lose = true
	if lifecycle.Reconcile(runctx, scope.ProjectID) == nil {
		t.Fatal("missing lost no-op fence reply")
	}
	converge()
	command("after-stopped-update", `set -eu; test "$(cat /data/knowledge.txt)" = knowledge-v2; test "$(cat retained)" = retained`)
	if wrapper.patches != 2 {
		t.Fatal("no-op cancellation fence repeated", wrapper.patches)
	}

	project, err = store.Project(runctx, scope)
	must(t, err)
	must(t, store.DeleteProject(runctx, scope, project.Revision))
	converge()
}
