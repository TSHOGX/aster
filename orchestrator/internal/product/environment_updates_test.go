package product

import (
	"aster/contracts/volumemanifest"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/testfixture"
	"testing"
	"time"
)

func publicationFixture(t *testing.T, s *Store, version string) ArtifactReference {
	t.Helper()
	entries := []content.Entry{{Path: version, Directory: true}}
	ref := ArtifactReference{ID: "dataset", Version: version, Digest: content.Digest(entries)}
	raw, _ := json.Marshal(entries)
	_, err := s.db.Exec(`INSERT INTO content_publications(store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,created_at,updated_at) VALUES('engine','builtin',?,1048576,?,?,?,?, 'published',?,?)`, ref.Digest, string(raw), "stage-"+version, "data-"+version, "data-"+version, s.timestamp(), s.timestamp())
	must(t, err)
	must(t, s.RegisterBuiltinVolume(ctx, BuiltinVolume{Reference: ref, StoreID: "engine", PublicationPurpose: "builtin", MinimumBytes: 1048576, Roles: []string{"data"}, Entries: entries}))
	return ref
}
func selectPublicationFixture(t *testing.T, s *Store, ref ArtifactReference, projects bool) string {
	t.Helper()
	var id, raw string
	var revision int64
	must(t, s.reader.QueryRow(`SELECT id,document,revision FROM environment_templates ORDER BY id LIMIT 1`).Scan(&id, &raw, &revision))
	var e DefaultEnvironment
	must(t, json.Unmarshal([]byte(raw), &e))
	mount := EnvironmentMount{Key: "data", Role: "data", Destination: "/data", Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: ref.ID, Version: ref.Version, Digest: ref.Digest}}
	found := false
	for i, m := range e.Mounts {
		if m.Key == mount.Key {
			e.Mounts[i] = mount
			found = true
		}
	}
	if !found {
		e.Mounts = append(e.Mounts, mount)
	}
	e.Revision = revision + 1
	body, _ := json.Marshal(e)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE environment_templates SET revision=?,document=? WHERE id=?`, e.Revision, string(body), id); err != nil {
			return err
		}
		if projects {
			_, err := tx.Exec(`UPDATE project_configuration SET environment=? WHERE environment_template_id=?`, string(body), id)
			return err
		}
		return nil
	}))
	return id
}
func publishUpdateFixture(t *testing.T, s *Store, token, id string) EnvironmentUpdate {
	t.Helper()
	tmpl, err := readTemplate(ctx, s.reader, "environment", id)
	must(t, err)
	input := EnvironmentUpdateEdit{RequestID: fmt.Sprintf("publish-%d", tmpl.Revision), Action: "publish", ExpectedRevision: tmpl.Revision, Preview: true}
	raw, err := s.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	var impact ConfigurationImpact
	must(t, json.Unmarshal(raw, &impact))
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	raw, err = s.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	var result struct {
		Release EnvironmentUpdate `json:"release"`
	}
	must(t, json.Unmarshal(raw, &result))
	return result.Release
}
func updateFixture(t *testing.T) (*Lifecycle, Scope, *fakeLifecycle, string, string) {
	s, account := accountStore(t)
	a := Scope{Actor: account.ID, ProjectID: "update-project"}
	_, err := s.CreateProject(ctx, a.Actor, a.ProjectID, "Update project")
	must(t, err)
	f := fakeBackend()
	l := &Lifecycle{Store: s, Backend: f, MaxProjects: 4, VerifyRuntime: testfixture.VerifyRuntime}
	token := adminTestToken(t, s, account)
	fixtureResourceLimit(t, s, account.ID, ResourceMounts, 8)
	first := publicationFixture(t, l.Store, "v1")
	id := selectPublicationFixture(t, l.Store, first, true)
	return l, a, f, token, id
}
func TestEnvironmentPublicationOnlyAppliesOnResumeAndUpdatesSlots(t *testing.T) {
	l, a, f, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	before, err := l.ActiveResources(ctx, a)
	must(t, err)
	second := publicationFixture(t, l.Store, "v2")
	selectPublicationFixture(t, l.Store, second, false)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	original, err := l.ActiveResources(ctx, a)
	must(t, err)
	if original.Volumes[0].VolumeID != before.Volumes[0].VolumeID {
		t.Fatal("template edit changed slots")
	}
	release := publishUpdateFixture(t, l.Store, token, id)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	if f.calls["network"] != 0 {
		t.Fatal("publication touched running placement")
	}
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	after, err := l.ActiveResources(ctx, a)
	must(t, err)
	if after.VolumeForRole("workspace") != before.VolumeForRole("workspace") || after.VolumeForRole("runtime") != before.VolumeForRole("runtime") {
		t.Fatal("owned volumes changed")
	}
	var data string
	for _, v := range after.Volumes {
		if v.Key == "data" {
			data = v.VolumeID
		}
	}
	if data != "data-v2" {
		t.Fatal("slot not updated", data)
	}
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.State != "current" || st.AppliedID != release.ID || st.Current[0].Source.Reference() != second {
		t.Fatal(st)
	}
	actual, err := f.InspectConfiguration(ctx, after.Reference)
	must(t, err)
	if !profileConfiguration(actual, after.Profile, after) {
		t.Fatal("engine binding differs")
	}
	if after.Profile.EnvironmentRevision != before.Profile.EnvironmentRevision {
		t.Fatal("data update claimed full environment revision")
	}
}
func TestEnvironmentPublicationLostReplyRetainsTarget(t *testing.T) {
	l, a, f, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	first := publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	f.fail["network"] = 1
	if l.Reconcile(ctx, a.ProjectID) == nil {
		t.Fatal("lost reply not injected")
	}
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v3"), false)
	second := publishUpdateFixture(t, l.Store, token, id)
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.AppliedID != first.ID || st.TargetID != second.ID || st.State != "pending" {
		t.Fatal(st)
	}
	if f.calls["network"] != 1 {
		t.Fatal("uncertain patch repeated")
	}
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err = updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.AppliedID != second.ID {
		t.Fatal(st)
	}
}
func TestEnvironmentPublicationUnavailableFallsBackWithoutUserFailure(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	second := publicationFixture(t, l.Store, "v2")
	selectPublicationFixture(t, l.Store, second, false)
	publishUpdateFixture(t, l.Store, token, id)
	_, err := l.Store.db.Exec(`UPDATE builtin_volumes SET enabled=0 WHERE id=? AND version=?`, second.ID, second.Version)
	must(t, err)
	for i := 0; i < 4; i++ {
		pausePolicyProject(t, l, a)
		must(t, l.Resume(ctx, a, testProfile().Bindings()))
		drive(t, l, a.ProjectID)
	}
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Attempts != 3 || st.State != "failed" || st.Current[0].Source.Version != "v1" {
		t.Fatal(st)
	}
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	if p.Failure != "" {
		t.Fatal("update leaked to daily UI", p.Failure)
	}
}

func TestEnvironmentPublicationRetryRecoversFailedDeployment(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	second := publicationFixture(t, l.Store, "retry-v2")
	selectPublicationFixture(t, l.Store, second, false)
	release := publishUpdateFixture(t, l.Store, token, id)
	_, err := l.Store.db.Exec(`UPDATE builtin_volumes SET enabled=0 WHERE id=? AND version=?`, second.ID, second.Version)
	must(t, err)
	for i := 0; i < 3; i++ {
		pausePolicyProject(t, l, a)
		must(t, l.Resume(ctx, a, testProfile().Bindings()))
		drive(t, l, a.ProjectID)
	}
	failed, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if failed.State != "failed" || failed.Attempts != 3 || failed.Current[0].Source.Version != "v1" {
		t.Fatal("deployment did not retain failed target", failed)
	}
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "retry", ReleaseID: release.ID, ExpectedRevision: release.Revision})
	retried, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if retried.State != "pending" || retried.Attempts != 0 || retried.TargetID != release.ID {
		t.Fatal("retry did not reopen the same deployment", retried)
	}
	_, err = l.Store.db.Exec(`UPDATE builtin_volumes SET enabled=1 WHERE id=? AND version=?`, second.ID, second.Version)
	must(t, err)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	ready, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if ready.State != "current" || ready.AppliedID != release.ID || ready.Current[0].Source.Version != "retry-v2" {
		t.Fatal("deployment retry did not converge", ready)
	}
}

func TestEnvironmentPublicationBeforeFirstStart(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	release := publishUpdateFixture(t, l.Store, token, id)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.AppliedID != release.ID || st.Current[0].Source.Version != "v2" {
		t.Fatal(st)
	}
}

func controlUpdateFixture(t *testing.T, s *Store, token, id string, input EnvironmentUpdateEdit) json.RawMessage {
	t.Helper()
	input.Preview = true
	if input.RequestID == "" {
		input.RequestID = fmt.Sprintf("%s-%d", input.Action, time.Now().UnixNano())
	}
	raw, err := s.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	var impact ConfigurationImpact
	must(t, json.Unmarshal(raw, &impact))
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	raw, err = s.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	replay, err := s.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	if string(raw) != string(replay) {
		t.Fatal("receipt replay changed")
	}
	return raw
}
func TestEnvironmentUpdateStopFixRollbackAndRetire(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	release := publishUpdateFixture(t, l.Store, token, id)
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "stop", ReleaseID: release.ID, ExpectedRevision: 1})
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Current[0].Source.Version != "v1" {
		t.Fatal(st)
	}
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "continue", ReleaseID: release.ID, ExpectedRevision: 2})
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "fix", ProjectIDs: []string{a.ProjectID}, ExpectedRevision: st.Revision})
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err = updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Current[0].Source.Version != "v1" || st.Policy != "fixed" {
		t.Fatal(st)
	}
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "follow", ProjectIDs: []string{a.ProjectID}, ExpectedRevision: st.Revision})
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v3"), false)
	publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	tmpl, err := readTemplate(ctx, l.Store.reader, "environment", id)
	must(t, err)
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "rollback", ReleaseID: release.ID, ExpectedRevision: tmpl.Revision})
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err = updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Current[0].Source.Version != "v2" {
		t.Fatal(st)
	}
	tmpl, err = readTemplate(ctx, l.Store.reader, "environment", id)
	must(t, err)
	if publicationMounts(*tmpl.Environment)[0].Source.Version != "v3" {
		t.Fatal("rollback changed new-project configuration")
	}
	old, err := readEnvironmentUpdate(ctx, l.Store.reader, release.ID)
	must(t, err)
	controlUpdateFixture(t, l.Store, token, id, EnvironmentUpdateEdit{Action: "retire", ReleaseID: old.ID, ExpectedRevision: old.Revision})
}
func TestEnvironmentUpdateReviewConflictAndUnallocatedCohort(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	tmpl, err := readTemplate(ctx, l.Store.reader, "environment", id)
	must(t, err)
	input := EnvironmentUpdateEdit{Action: "publish", Preview: true, RequestID: "review", ExpectedRevision: tmpl.Revision}
	raw, err := l.Store.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	var impact ConfigurationImpact
	must(t, json.Unmarshal(raw, &impact))
	_, err = l.Store.CreateProject(ctx, a.Actor, "later-project", "Later")
	must(t, err)
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	_, err = l.Store.ChangeEnvironmentUpdate(ctx, token, id, input)
	wantError(t, err, ErrConflict)
	release := publishUpdateFixture(t, l.Store, token, id)
	_, err = l.Store.CreateProject(ctx, a.Actor, "after-publication", "After")
	must(t, err)
	st, err := updateStatus(ctx, l.Store.reader, "after-publication")
	must(t, err)
	if st.TargetID != "" {
		t.Fatal("new project enrolled in older publication", st)
	}
	page, err := l.Store.PreviewEnvironmentProjects(ctx, token, id, "", "", "", 1)
	must(t, err)
	if len(page.Projects) != 1 || page.NextCursor == "" {
		t.Fatal(page)
	}
	if release.Kind != "publication_replace" {
		t.Fatal(release)
	}
}
func TestEnvironmentUpdateStructuralConflictAndHistoryReferences(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	v2 := publicationFixture(t, l.Store, "v2")
	selectPublicationFixture(t, l.Store, v2, false)
	release := publishUpdateFixture(t, l.Store, token, id)
	before, err := registryState(ctx, l.Store.reader, "volume", v2)
	must(t, err)
	if !before.TemplateReferenced {
		t.Fatal("publication not pinned")
	}
	c, err := projectConfiguration(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	shifted := append([]EnvironmentMount(nil), release.Mounts...)
	shifted[0].Destination = "/different"
	_, err = publicationCompatible(ctx, l.Store.reader, c.Environment, shifted)
	wantError(t, err, ErrConflict)
	_, err = publicationCompatible(ctx, l.Store.reader, c.Environment, nil)
	wantError(t, err, ErrConflict)
	_, err = l.Store.db.Exec(`UPDATE environment_updates SET mounts='[]' WHERE id=?`, release.ID)
	if err == nil {
		t.Fatal("immutable publication was changed")
	}
}
func TestEnvironmentUpdateHeldLostPatchReconcilesBeforeCancellation(t *testing.T) {
	for _, issued := range []bool{false, true} {
		t.Run(fmt.Sprint(issued), func(t *testing.T) {
			l, a, f, token, id := updateFixture(t)
			must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
			drive(t, l, a.ProjectID)
			selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
			publishUpdateFixture(t, l.Store, token, id)
			pausePolicyProject(t, l, a)
			must(t, l.Resume(ctx, a, testProfile().Bindings()))
			if issued {
				f.fail["network"] = 1
				if l.Reconcile(ctx, a.ProjectID) == nil {
					t.Fatal("expected lost response")
				}
			}
			_, err := l.Store.db.Exec(`UPDATE projects SET execution_suspended=1,execution_hold_reason='account_suspended' WHERE id=?`, a.ProjectID)
			must(t, err)
			must(t, l.Reconcile(ctx, a.ProjectID))
			var pending int
			must(t, l.Store.reader.QueryRow(`SELECT count(*) FROM environment_update_executions WHERE state IN ('pending','unknown')`).Scan(&pending))
			if pending != 0 {
				t.Fatal("held execution leaked")
			}
			r, err := l.Store.Resources(ctx, a)
			must(t, err)
			actual, err := f.InspectConfiguration(ctx, r.Reference)
			must(t, err)
			if !profileConfiguration(actual, r.Profile, r) {
				t.Fatal("hold canceled uncertain target without adopting engine result")
			}
			if f.calls["resume"] != 0 {
				t.Fatal("held project resumed")
			}
		})
	}
}
func TestEnvironmentUpdateDeletionDoesNotResume(t *testing.T) {
	for _, issued := range []bool{false, true} {
		t.Run(fmt.Sprint(issued), func(t *testing.T) {
			l, a, f, token, id := updateFixture(t)
			must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
			drive(t, l, a.ProjectID)
			selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
			publishUpdateFixture(t, l.Store, token, id)
			pausePolicyProject(t, l, a)
			must(t, l.Resume(ctx, a, testProfile().Bindings()))
			if issued {
				f.fail["network"] = 1
				if l.Reconcile(ctx, a.ProjectID) == nil {
					t.Fatal("expected lost response")
				}
			}
			p, err := l.Store.Project(ctx, a)
			must(t, err)
			must(t, l.Store.DeleteProject(ctx, a, p.Revision))
			drive(t, l, a.ProjectID)
			if f.calls["resume"] != 0 {
				t.Fatal("deleting project resumed")
			}
			var count int
			must(t, l.Store.reader.QueryRow(`SELECT count(*) FROM environment_update_executions WHERE project_id=?`, a.ProjectID).Scan(&count))
			if count != 0 {
				t.Fatal("deletion retained execution references")
			}
		})
	}
}

type missingUpdateVolume struct{ *fakeLifecycle }

func (b missingUpdateVolume) InspectVolume(_ context.Context, ref sandbox.VolumeReference) (sandbox.Volume, error) {
	if ref.VolumeID == "data-v2" {
		return sandbox.Volume{}, &sandbox.Error{Class: sandbox.NotFound, Operation: "inspect update"}
	}
	return sandbox.Volume{VolumeReference: ref, Capacity: 1 << 20}, nil
}
func TestEnvironmentFirstStartUnavailableTargetAttemptsOnlyOnce(t *testing.T) {
	l, a, f, token, id := updateFixture(t)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	publishUpdateFixture(t, l.Store, token, id)
	l.Backend = missingUpdateVolume{f}
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Attempts != 1 || st.State != "deferred" || st.Current[0].Source.Version != "v1" {
		t.Fatal(st)
	}
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	if p.State != "active" || p.Failure != "" {
		t.Fatal(p)
	}
}
func TestEnvironmentUnknownSourceCancellationFencesDelayedPatch(t *testing.T) {
	l, a, f, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	o, err := pendingOperation(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	var original operationRequest
	must(t, json.Unmarshal([]byte(o.Request), &original))
	// Simulate a PATCH lost before the engine received it; it may arrive later.
	if err = l.unknown(ctx, o, errors.New("delayed original request")); err == nil {
		t.Fatal("missing injected uncertainty")
	}
	_, err = l.Store.db.Exec(`UPDATE projects SET execution_suspended=1,execution_hold_reason='held' WHERE id=?`, a.ProjectID)
	must(t, err)
	f.fail["network"] = 1 // Lose the response to the cancellation fence as well.
	if l.Reconcile(ctx, a.ProjectID) == nil {
		t.Fatal("missing lost fence response")
	}
	must(t, l.Reconcile(ctx, a.ProjectID))
	r, err := l.Store.Resources(ctx, a)
	must(t, err)
	if r.Reference.MutationRevision == original.Reference.MutationRevision {
		t.Fatal("delayed patch was not fenced")
	}
	target := r.Profile
	target.Volumes = original.Volumes
	mounts, err := configurationResources(r, target, true).Mounts()
	must(t, err)
	_, err = f.UpdateConfiguration(ctx, original.Reference, target.Reachability.Declaration(), mounts)
	wantError(t, err, ErrConflict)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Current[0].Source.Version != "v1" || st.State != "pending" {
		t.Fatal(st)
	}
	if f.calls["network"] != 1 {
		t.Fatal("cancellation fence was replayed", f.calls)
	}
}

func TestEnvironmentUnknownAttemptWorkerBackoff(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	o, err := pendingOperation(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if l.unknown(ctx, o, errors.New("uncertain target")) == nil {
		t.Fatal("expected uncertainty")
	}
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	w := Workers{Store: l.Store} // Backoff must return before touching any engine port.
	must(t, w.project(ctx, p))
	var retryAfter string
	var attempts int
	must(t, l.Store.reader.QueryRow(`SELECT retry_after,recovery_attempts FROM environment_update_executions WHERE project_id=?`, a.ProjectID).Scan(&retryAfter, &attempts))
	if retryAfter <= l.Store.timestamp() || attempts != 1 {
		t.Fatal(retryAfter, attempts)
	}
}

func TestEnvironmentEmptyReleasePreviewExplainsWithoutPublishing(t *testing.T) {
	l, _, _, token, id := updateFixture(t)
	tmpl, err := readTemplate(ctx, l.Store.reader, "environment", id)
	must(t, err)
	input := EnvironmentUpdateEdit{RequestID: "empty-preview", Action: "publish", Preview: true, ExpectedRevision: tmpl.Revision}
	raw, err := l.Store.ChangeEnvironmentUpdate(ctx, token, id, input)
	must(t, err)
	var impact struct {
		ExpectedDependencies string `json:"expected_dependencies"`
		Proposed             struct {
			CanPublish bool           `json:"can_publish"`
			Counts     map[string]int `json:"counts"`
		} `json:"proposed"`
	}
	must(t, json.Unmarshal(raw, &impact))
	if impact.Proposed.CanPublish || impact.Proposed.Counts["current"] != 1 {
		t.Fatal(string(raw))
	}
	input.Preview = false
	input.ExpectedDependencies = impact.ExpectedDependencies
	_, err = l.Store.ChangeEnvironmentUpdate(ctx, token, id, input)
	wantError(t, err, ErrConflict)
	var count int
	must(t, l.Store.reader.QueryRow(`SELECT count(*) FROM environment_updates`).Scan(&count))
	if count != 0 {
		t.Fatal("empty publication persisted")
	}
}

func readyUpdateImport(t *testing.T, s *Store, token, source, publisher, library, version string) VolumeImport {
	t.Helper()
	descriptor, err := manifest.New(publisher, library, version, []manifest.Entry{{Path: library + "-" + version, Kind: "directory", Size: "0"}})
	must(t, err)
	raw, err := descriptor.Canonical()
	must(t, err)
	digest, err := descriptor.Digest()
	must(t, err)
	i, err := s.AdmitVolumeImport(ctx, token, VolumeImportInput{RequestID: source + "-" + version, SourceID: source, VersionID: version, ExpectedManifestDigest: digest, ConsumerProfile: manifest.Profile}, raw, "engine", "issuer/publisher", 2<<30, 2<<30, VolumeImportLabels{LibraryName: library, VersionLabel: version})
	must(t, err)
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "acquiring", "source_acquired", ""))
	must(t, s.AdvanceVolumeImport(ctx, i.ID, "source_acquired", "materializing", ""))
	revision, err := i.Revision(nil)
	must(t, err)
	p, err := s.ClaimContentPublication(ctx, content.Publication{StoreID: i.StoreID, Purpose: "authored", RevisionDigest: i.TreeDigest, Capacity: i.Capacity, Entries: revision.Entries})
	must(t, err)
	p, err = s.RecordContentStaging(ctx, p, "stage-"+i.ID)
	must(t, err)
	p, err = s.StartContentPopulation(ctx, p)
	must(t, err)
	p, err = s.FinishContentPopulation(ctx, p)
	must(t, err)
	p, err = s.RecordContentDerivation(ctx, p, "frozen-"+i.ID)
	must(t, err)
	_, err = s.CommitContentPublication(ctx, p, "frozen-"+i.ID, i.TreeDigest)
	must(t, err)
	must(t, s.FinalizeVolumeImport(ctx, i.ID))
	return i
}
func TestEnvironmentStudioLibraryIdentityAndCrossLibraryRejection(t *testing.T) {
	l, a, _, token, id := updateFixture(t)
	first := readyUpdateImport(t, l.Store, token, "studio", "publisher", "library", "v1")
	selectPublicationFixture(t, l.Store, first.Artifact, true)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	second := readyUpdateImport(t, l.Store, token, "studio", "publisher", "library", "v2")
	if first.Artifact.ID == second.Artifact.ID {
		t.Fatal("fixture must use actual per-import artifact identities")
	}
	selectPublicationFixture(t, l.Store, second.Artifact, false)
	publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.Current[0].Source.Reference() != second.Artifact || st.Labels[second.Artifact.ID+"@v2"].LibraryName != "library" {
		t.Fatal(st)
	}
	other := readyUpdateImport(t, l.Store, token, "studio", "publisher", "different", "v3")
	selectPublicationFixture(t, l.Store, other.Artifact, false)
	impact, err := l.Store.PreviewEnvironmentProjects(ctx, token, id, "", "", "", 20)
	must(t, err)
	if impact.Counts["incompatible"] != 1 || len(impact.Projects) != 1 {
		t.Fatal(impact)
	}
	// The original import is no longer pinned by the audit-only creation snapshot.
	original, err := registryState(ctx, l.Store.reader, "volume", first.Artifact)
	must(t, err)
	if original.TemplateReferenced || original.ProjectReferences != 0 || original.PendingReferences != 0 {
		t.Fatal("old data remained permanently pinned", original)
	}
}

func TestEnvironmentUnknownFallbackFencesBeforeUsingOldVersion(t *testing.T) {
	l, a, f, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	second := publicationFixture(t, l.Store, "v2")
	selectPublicationFixture(t, l.Store, second, false)
	publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	o, err := pendingOperation(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	var original operationRequest
	must(t, json.Unmarshal([]byte(o.Request), &original))
	if l.unknown(ctx, o, errors.New("original patch may arrive later")) == nil {
		t.Fatal("missing uncertainty")
	}
	_, err = l.Store.db.Exec(`UPDATE builtin_volumes SET enabled=0 WHERE id=? AND version=?`, second.ID, second.Version)
	must(t, err)
	f.fail["network"] = 1
	if l.Reconcile(ctx, a.ProjectID) == nil {
		t.Fatal("missing lost cancellation-fence reply")
	}
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.State != "deferred" || st.Attempts != 1 || st.Current[0].Source.Version != "v1" {
		t.Fatal(st)
	}
	r, err := l.ActiveResources(ctx, a)
	must(t, err)
	if r.Reference.MutationRevision == original.Reference.MutationRevision || f.calls["network"] != 1 {
		t.Fatal("fallback did not fence exactly once", f.calls)
	}
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	if p.Failure != "" {
		t.Fatal("safe fallback exposed update failure")
	}
}
func TestEnvironmentDatabaseCommitFailureAdoptsObservedTarget(t *testing.T) {
	l, a, f, token, id := updateFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	selectPublicationFixture(t, l.Store, publicationFixture(t, l.Store, "v2"), false)
	publishUpdateFixture(t, l.Store, token, id)
	pausePolicyProject(t, l, a)
	must(t, l.Resume(ctx, a, testProfile().Bindings()))
	_, err := l.Store.db.Exec(`CREATE TRIGGER fail_update_commit BEFORE UPDATE OF profile ON project_resources BEGIN SELECT RAISE(ABORT,'injected update commit failure'); END`)
	must(t, err)
	if l.Reconcile(ctx, a.ProjectID) == nil {
		t.Fatal("commit failure not injected")
	}
	_, err = l.Store.db.Exec(`DROP TRIGGER fail_update_commit`)
	must(t, err)
	drive(t, l, a.ProjectID)
	st, err := updateStatus(ctx, l.Store.reader, a.ProjectID)
	must(t, err)
	if st.State != "current" || st.Current[0].Source.Version != "v2" || f.calls["network"] != 1 {
		t.Fatal(st, f.calls)
	}
}
