package product

import (
	"harness-platform/orchestrator/internal/content"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseActivationRequiresReplacementIdentityAndSettledWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activation.db")
	s, err := Open(ctx, path)
	must(t, err)
	scope := Scope{"owner", "project"}
	_, err = s.CreateProject(ctx, scope.Actor, scope.ProjectID, "Project")
	must(t, err)
	_, err = s.CreateSession(ctx, scope, "session", "Session")
	must(t, err)
	held, err := s.AdmitTurn(ctx, scope, "session", "saved", "retained intent", true)
	must(t, err)
	drain, err := s.BeginReleaseDrain(ctx, "activation-owner", time.Minute)
	must(t, err)
	target := "sha256:" + strings.Repeat("a", 64)
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target)
	wantError(t, err, ErrConflict)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	active, err := s.ActivateRelease(ctx, drain.Owner, drain.Generation, target)
	must(t, err)
	if active.Phase != "activating" || active.ActivationInstance == "" || active.TargetDigest != target {
		t.Fatal(active)
	}
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target)
	wantError(t, err, ErrConflict)
	_, err = s.AbortReleaseDrain(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrConflict)
	_, err = s.beginRuntimeWork(ctx, runtimeMaintenance)
	wantError(t, err, ErrDraining)
	deadline, err := time.Parse(time.RFC3339Nano, active.Deadline)
	must(t, err)
	s.now = func() time.Time { return deadline.Add(time.Hour) }
	must(t, s.ExpireReleaseDrain(ctx))
	still, err := s.ReleaseState(ctx)
	must(t, err)
	if still.Phase != "activating" {
		t.Fatal(still)
	}
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, "sha256:"+strings.Repeat("b", 64))
	wantError(t, err, ErrConflict)
	work, err := s.beginRuntimeWork(ctx, runtimeMaintenance)
	must(t, err)
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target)
	wantError(t, err, ErrBusy)
	work()
	must(t, s.BeginActivity(ctx, scope, Activity{ID: "late-cleanup", SessionID: "session", Kind: "descendants"}))
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target)
	wantError(t, err, ErrBusy)
	must(t, s.SettleActivity(ctx, scope.ProjectID, "late-cleanup", "complete"))
	finished, err := s.FinishRelease(ctx, drain.Owner, drain.Generation, target)
	must(t, err)
	if finished.Phase != "open" || finished.FinishedDigest != target || finished.Reason != "completed" {
		t.Fatal(finished)
	}
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target)
	must(t, err)
	rows, err := s.Turns(ctx, scope, "session", 0, 100)
	must(t, err)
	if len(rows) != 1 || rows[0].ID != held.ID || rows[0].State != "held" {
		t.Fatal(rows)
	}
}

func TestReleaseCompletionAndCoordinatorReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completion.db")
	s, err := Open(ctx, path)
	must(t, err)
	target := "sha256:" + strings.Repeat("c", 64)
	drain, err := s.BeginReleaseDrain(ctx, "original", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target)
	must(t, err)
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	completedDigest := target
	result, err := s.FinishRelease(ctx, drain.Owner, drain.Generation, completedDigest)
	must(t, err)
	if result.Reason != "completed" || result.FinishedDigest != completedDigest {
		t.Fatal(result)
	}
	next, err := s.BeginReleaseDrain(ctx, "second", time.Minute)
	must(t, err)
	_, err = s.AbortReleaseDrain(ctx, next.Owner, next.Generation)
	must(t, err)
	_, err = s.BeginReleaseDrain(ctx, "original", time.Minute)
	wantError(t, err, ErrConflict)
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, completedDigest)
	wantError(t, err, ErrConflict)
}

func TestReplacementCanPublishButRetiringOwnerCannot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication.db")
	s, err := Open(ctx, path)
	must(t, err)
	drain, err := s.BeginReleaseDrain(ctx, "publisher", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	must(t, err)
	target := "sha256:" + strings.Repeat("e", 64)
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target)
	must(t, err)
	entries := []content.Entry{{Path: "assets", Directory: true}}
	publication := content.Publication{StoreID: "engine", Purpose: "assets", RevisionDigest: content.Digest(entries), Capacity: 1024, Entries: entries}
	_, err = s.ClaimContentPublication(ctx, publication)
	wantError(t, err, ErrDraining)
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	_, err = s.ClaimContentPublication(ctx, publication)
	must(t, err)
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target)
	wantError(t, err, ErrBusy)
}
