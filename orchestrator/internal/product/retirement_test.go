package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type failingArtifacts struct {
	delegate ProjectArtifacts
	fail     bool
}

func (a *failingArtifacts) RemoveProject(ctx context.Context, id string) error {
	if a.fail {
		return errors.New("archive storage unavailable")
	}
	return a.delegate.RemoveProject(ctx, id)
}
func TestProjectDeletionWaitsForArtifactsAndPurgesOwnedHistory(t *testing.T) {
	files, scope, _ := fileFixture(t)
	s := files.Store
	l := files.Lifecycle
	_, err := s.CreateSession(ctx, scope, "agent", "Retired history")
	must(t, err)
	seedMessage(t, s, scope, "agent", "message", "private conversation")
	shares := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	share, token, err := shares.Create(ctx, scope, "agent", nil, time.Now().Add(time.Hour))
	must(t, err)
	captures := &Captures{Store: s, Directory: t.TempDir()}
	archive := filepath.Join(captures.Directory, scope.ProjectID, "agent")
	must(t, os.MkdirAll(archive, 0700))
	must(t, os.WriteFile(filepath.Join(archive, "interrupted.tmp"), []byte("private history"), 0600))
	artifacts := &failingArtifacts{delegate: &Artifacts{Store: s, Captures: captures, Shares: shares}, fail: true}
	l.Artifacts = artifacts
	p, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	_, err = s.PublicShare(ctx, token)
	wantError(t, err, ErrNotFound)
	for n := 0; n < 8; n++ {
		_ = l.Reconcile(ctx, scope.ProjectID)
	}
	p, err = s.Project(ctx, scope)
	must(t, err)
	if p.State != "deleting" {
		t.Fatal("completed deletion before artifact reclamation", p)
	}
	r, err := s.Resources(ctx, scope)
	must(t, err)
	if r.Occupied {
		t.Fatal("verified sandbox deletion retained capacity")
	}
	if _, err = os.Stat(archive); err != nil {
		t.Fatal("failed cleanup removed unexpected data", err)
	}
	artifacts.fail = false
	drive(t, l, scope.ProjectID)
	p, err = s.Project(ctx, scope)
	must(t, err)
	if p.State != "deleted" {
		t.Fatal(p)
	}
	for _, directory := range []string{archive, filepath.Join(shares.Directory, share.ID)} {
		if _, err = os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("owned artifact remains", directory, err)
		}
	}
	for _, table := range []string{"messages", "turns", "shares", "notifications", "project_activity", "agent_commands"} {
		var count int
		must(t, s.reader.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		if count != 0 {
			t.Fatal("retained private payload", table, count)
		}
	}
	child, err := s.Session(ctx, scope, "agent")
	must(t, err)
	if child.Deletion != Settled || child.Title != "Deleted Session" {
		t.Fatal(child)
	}
}
func TestNeverProvisionedProjectStillReclaimsShares(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "empty", "Empty")
	must(t, err)
	shares := &Shares{Store: s, Directory: t.TempDir()}
	share, _, err := shares.Create(ctx, scope, "empty", nil, time.Now().Add(time.Hour))
	must(t, err)
	l := &Lifecycle{Store: s, Artifacts: &Artifacts{Store: s, Shares: shares}}
	p, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	drive(t, l, scope.ProjectID)
	if _, err = os.Stat(filepath.Join(shares.Directory, share.ID)); !os.IsNotExist(err) {
		t.Fatal("orphaned never-provisioned share directory", err)
	}
}
func TestObservedPlacementLossAndStoreChangeRemainScoped(t *testing.T) {
	s, a, _ := fixture(t)
	backend := fakeBackend()
	l := &Lifecycle{Store: s, Backend: backend, MaxProjects: 1}
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	r, err := s.Resources(ctx, a)
	must(t, err)
	backend.mu.Lock()
	delete(backend.sandboxes, r.Reference.SandboxID)
	backend.mu.Unlock()
	must(t, l.Observe(ctx, a.ProjectID))
	current, err := s.Resources(ctx, a)
	must(t, err)
	if current.Occupied {
		t.Fatal("absent placement was not released")
	}
	drive(t, l, a.ProjectID)
	current, err = s.Resources(ctx, a)
	must(t, err)
	if current.Reference.SandboxID == r.Reference.SandboxID || current.WorkspaceVolume != r.WorkspaceVolume || current.RuntimeVolume != r.RuntimeVolume {
		t.Fatal("replacement did not preserve Project volumes", current, r)
	}
	// Changed defaults cannot rewrite or strand a Project's saved profile.
	changed := testProfile()
	changed.MemoryBytes *= 2
	must(t, l.EnsureConfiguredActive(ctx, a, changed))
	backend.mu.Lock()
	backend.store = "different-store"
	backend.mu.Unlock()
	_, err = s.db.Exec(`UPDATE project_resources SET observed_at='' WHERE project_id=?`, a.ProjectID)
	must(t, err)
	must(t, l.Observe(ctx, a.ProjectID))
	p, err := s.Project(ctx, a)
	must(t, err)
	if p.State != "blocked" {
		t.Fatal(p)
	}
	current, err = s.Resources(ctx, a)
	must(t, err)
	if !current.Occupied {
		t.Fatal("store contradiction released uncertain capacity")
	}

}

func TestSessionRetirementKeepsProjectScheduleAndSiblingRuns(t *testing.T) {
	s, scope, _ := fixture(t)
	for _, id := range []string{"alpha", "beta"} {
		_, err := s.CreateSession(ctx, scope, id, id)
		must(t, err)
	}
	schedule, err := s.CreateSchedule(ctx, scope, Schedule{TriggerKind: "manual", ID: "schedule", SessionID: "beta", Title: "Project-owned schedule", Prompt: "work", Timezone: "UTC", State: "active"})
	must(t, err)
	siblingRun, err := s.RunSchedule(ctx, scope, schedule.ID, "beta-run")
	must(t, err)
	schedule.SessionID = "alpha"
	must(t, s.UpdateSchedule(ctx, scope, schedule, false))
	_, err = s.RunSchedule(ctx, scope, schedule.ID, "alpha-run")
	must(t, err)
	child, err := s.Session(ctx, scope, "alpha")
	must(t, err)
	must(t, s.DeleteSession(ctx, scope, child.ID, child.Revision))
	retirement := &ChildRetirement{Store: s}
	must(t, retirement.Step(ctx, Child{ProjectID: scope.ProjectID, ID: "alpha", Kind: "session", Deletion: Requested}))
	retained, err := s.Schedule(ctx, scope, schedule.ID)
	must(t, err)
	if retained.State == "active" {
		t.Fatal("retired target's schedule still enabled")
	}
	runs, err := s.ScheduleRuns(ctx, scope, schedule.ID, "", 100)
	must(t, err)
	if len(runs) != 1 || runs[0].ID != siblingRun.ID || runs[0].State != "queued" {
		t.Fatal("Session cleanup removed sibling run history", runs)
	}
}
