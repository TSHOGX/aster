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
		if r.Operation == "write" {
			_, err = f.Upload(ctx, scope, "write", r, strings.NewReader("new bytes"))
		} else {
			_, err = f.Manage(ctx, scope, []string{"dir", "write", "move"}[i], r)
		}
		must(t, err)
	}
	got, err := os.ReadFile(filepath.Join(b.root, "reports/final.txt"))
	must(t, err)
	if string(got) != "new bytes" {
		t.Fatal(string(got))
	}
	_, err = f.Upload(ctx, scope, "write", writeRequest("reports/note.txt", "new bytes"), strings.NewReader("new bytes"))
	must(t, err)
	if _, err = os.Stat(filepath.Join(b.root, "reports/note.txt")); !os.IsNotExist(err) {
		t.Fatal("replayed direct mutation", err)
	}
	_, err = f.Upload(ctx, scope, "bad-hash", writeRequest("reports/final.txt", "wrong"), strings.NewReader("other"))
	wantError(t, err, ErrFileOperationFailed)
	got, err = os.ReadFile(filepath.Join(b.root, "reports/final.txt"))
	must(t, err)
	if string(got) != "new bytes" {
		t.Fatal("invalid upload replaced original")
	}
	_, err = f.Manage(ctx, scope, "remove", projectfiles.Request{Operation: "remove", Path: "reports/final.txt"})
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
	page, err := f.List(ctx, scope, projectfiles.DirectoryQuery{Path: "", Cursor: "", Limit: 100})
	must(t, err)
	if len(page.Entries) != 0 {
		t.Fatal(page)
	}
	_, err = f.Upload(ctx, scope, "upload", writeRequest("new.txt", "created without compute"), strings.NewReader("created without compute"))
	must(t, err)
	r, err := f.Store.Resources(ctx, scope)
	must(t, err)
	if r.Occupied || r.Reference.SandboxID != "" || r.VolumeForRole("runtime") != "" || r.VolumeForRole("workspace") == "" || b.starts != 0 {
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
	workspace := r.VolumeForRole("workspace")
	f.Lifecycle.MaxProjects = 4
	must(t, f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile))
	drive(t, f.Lifecycle, scope.ProjectID)
	r, err = f.Store.Resources(ctx, scope)
	must(t, err)
	if r.VolumeForRole("workspace") != workspace || r.Reference.SandboxID == "" {
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
				_, e := f.Upload(ctx, scope, "pending", writeRequest("file", "bytes"), strings.NewReader("bytes"))
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
			_, err = f.Upload(ctx, scope, "pending", writeRequest("file", "bytes"), strings.NewReader("bytes"))
			wantError(t, err, ErrFileOutcomeUnknown)
		})
	}
}

type fullVolumeBackend struct{ *localFileBackend }

func (b *fullVolumeBackend) ImportVolume(ctx context.Context, ref sandbox.VolumeReference, name string, input io.Reader, size int64) error {
	if err := b.WriteVolume(ctx, ref, name, []byte("partial")); err != nil {
		return err
	}
	return &sandbox.Error{Class: sandbox.Capacity, Cause: errors.New("volume full")}
}
func TestPausedVolumeQuotaFailureKeepsTargetAndReclaimsStaging(t *testing.T) {
	f, scope, b := fileFixture(t)
	must(t, os.WriteFile(filepath.Join(b.root, "target"), []byte("original"), 0600))
	p, err := f.Store.Project(ctx, scope)
	must(t, err)
	must(t, f.Lifecycle.Pause(ctx, scope, p.Revision))
	drive(t, f.Lifecycle, scope.ProjectID)
	f.Backend = &fullVolumeBackend{b}
	op, err := f.Upload(ctx, scope, "full", writeRequest("target", "replacement"), strings.NewReader("replacement"))
	wantError(t, err, ErrFileOperationFailed)
	if op.State != "failed" || !op.Settled {
		t.Fatal(op)
	}
	data, err := os.ReadFile(filepath.Join(b.root, "target"))
	must(t, err)
	if string(data) != "original" {
		t.Fatal(string(data))
	}
	entries, err := os.ReadDir(b.root)
	must(t, err)
	if len(entries) != 1 || entries[0].Name() != "target" {
		t.Fatal("quota failure leaked staging", entries)
	}
	_, err = f.Manage(ctx, scope, "free-space", projectfiles.Request{Operation: "remove", Path: "target"})
	must(t, err)
	must(t, f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile))
}
