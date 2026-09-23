package product

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPausedVolumeMutationsRetainComputeReleaseAndReplayIdentity(t *testing.T) {
	f, scope, b := fileFixture(t)
	p, err := f.Store.Project(ctx, scope)
	must(t, err)
	must(t, f.Lifecycle.Pause(ctx, scope, p.Revision))
	drive(t, f.Lifecycle, scope.ProjectID)
	for i, r := range []projectfiles.Request{{Operation: "mkdir", Path: "reports"}, writeRequest("reports/note.txt", "new bytes"), {Operation: "move", Path: "reports/note.txt", Destination: "reports/final.txt"}} {
		_, err = f.Execute(ctx, scope, []string{"dir", "write", "move"}[i], r, strings.NewReader("new bytes"))
		must(t, err)
	}
	got, err := os.ReadFile(filepath.Join(b.root, "reports/final.txt"))
	must(t, err)
	if string(got) != "new bytes" {
		t.Fatal(string(got))
	}
	_, err = f.Execute(ctx, scope, "write", writeRequest("reports/note.txt", "new bytes"), strings.NewReader("new bytes"))
	must(t, err)
	if _, err = os.Stat(filepath.Join(b.root, "reports/note.txt")); !os.IsNotExist(err) {
		t.Fatal("replayed direct mutation", err)
	}
	_, err = f.Execute(ctx, scope, "bad-hash", writeRequest("reports/final.txt", "wrong"), strings.NewReader("other"))
	wantError(t, err, ErrFileOperationFailed)
	got, err = os.ReadFile(filepath.Join(b.root, "reports/final.txt"))
	must(t, err)
	if string(got) != "new bytes" {
		t.Fatal("invalid upload replaced original")
	}
	_, err = f.Execute(ctx, scope, "remove", projectfiles.Request{Operation: "remove", Path: "reports/final.txt"}, nil)
	must(t, err)
	p, err = f.Store.Project(ctx, scope)
	must(t, err)
	r, err := f.Store.Resources(ctx, scope)
	must(t, err)
	if p.State != "paused" || r.Occupied || b.starts != 0 {
		t.Fatal("volume mutations activated compute", p, r, b.starts)
	}
}
func TestFreshProjectCanUploadAndBrowseBeforeFirstComputeStart(t *testing.T) {
	f, _, b := fileFixture(t)
	scope := Scope{Actor: "owner", ProjectID: "storage-only"}
	_, err := f.Store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Storage")
	must(t, err)
	page, err := f.List(ctx, scope, "", "", 100)
	must(t, err)
	if len(page.Entries) != 0 {
		t.Fatal(page)
	}
	_, err = f.Execute(ctx, scope, "upload", writeRequest("new.txt", "created without compute"), strings.NewReader("created without compute"))
	must(t, err)
	r, err := f.Store.Resources(ctx, scope)
	must(t, err)
	if r.Occupied || r.Reference.SandboxID != "" || r.RuntimeVolume != "" || r.WorkspaceVolume == "" || b.starts != 0 {
		t.Fatal(r)
	}
	capture, err := f.Capture(ctx, scope, "new.txt")
	must(t, err)
	data, err := io.ReadAll(capture)
	must(t, err)
	must(t, capture.Close())
	if string(data) != "created without compute" {
		t.Fatal(string(data))
	}
	workspace := r.WorkspaceVolume
	f.Lifecycle.MaxProjects = 4
	must(t, f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile))
	drive(t, f.Lifecycle, scope.ProjectID)
	r, err = f.Store.Resources(ctx, scope)
	must(t, err)
	if r.WorkspaceVolume != workspace || r.Reference.SandboxID == "" {
		t.Fatal("first compute replaced workspace", r)
	}
}

type blockedVolumeBackend struct {
	*localFileBackend
	entered, continueWrite chan struct{}
	uncertain              bool
}

func (b *blockedVolumeBackend) ImportVolume(ctx context.Context, ref sandbox.VolumeReference, name string, input io.Reader, size int64) error {
	close(b.entered)
	select {
	case <-b.continueWrite:
	case <-ctx.Done():
		return ctx.Err()
	}
	if b.uncertain {
		return &sandbox.Error{Class: sandbox.Unavailable, Cause: errors.New("lost response")}
	}
	return b.localFileBackend.ImportVolume(ctx, ref, name, input, size)
}
func TestPausedUploadFencesResumeAndPreservesUnknownOutcome(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "uncertain"}[uncertain], func(t *testing.T) {
			f, scope, b := fileFixture(t)
			p, err := f.Store.Project(ctx, scope)
			must(t, err)
			must(t, f.Lifecycle.Pause(ctx, scope, p.Revision))
			drive(t, f.Lifecycle, scope.ProjectID)
			blocked := &blockedVolumeBackend{localFileBackend: b, entered: make(chan struct{}), continueWrite: make(chan struct{}), uncertain: uncertain}
			f.Backend = blocked
			result := make(chan error, 1)
			go func() {
				_, e := f.Execute(ctx, scope, "pending", writeRequest("file", "bytes"), strings.NewReader("bytes"))
				result <- e
			}()
			select {
			case <-blocked.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("upload did not start")
			}
			wantError(t, f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile), ErrBusy)
			close(blocked.continueWrite)
			err = <-result
			if !uncertain {
				must(t, err)
				must(t, f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile))
				return
			}
			wantError(t, err, ErrFileOutcomeUnknown)
			must(t, f.Reconcile(ctx, 32))
			op, err := f.Store.FileOperation(ctx, scope, "pending")
			must(t, err)
			if op.State != "unknown" || op.Settled {
				t.Fatal(op)
			}
			wantError(t, f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile), ErrBusy)
			_, err = f.Execute(ctx, scope, "pending", writeRequest("file", "bytes"), strings.NewReader("bytes"))
			wantError(t, err, ErrFileOutcomeUnknown)
		})
	}
}
