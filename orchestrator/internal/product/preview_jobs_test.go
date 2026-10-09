package product

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreviewJobsIsolationWindowsDeduplicationAndCleanup(t *testing.T) {
	worker := os.Getenv("PREVIEW_TEST_WORKER")
	if worker == "" {
		t.Skip("set PREVIEW_TEST_WORKER to a built static preview-worker to exercise Linux namespaces")
	}
	f, scope, backend := fileFixture(t)
	file, err := os.Create(filepath.Join(backend.root, "file.docx"))
	must(t, err)
	archive := zip.NewWriter(file)
	entry, err := archive.Create("word/document.xml")
	must(t, err)
	_, err = entry.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Initial content</w:t></w:r></w:p><w:p><w:r><w:t>Final content</w:t></w:r></w:p></w:body></w:document>`))
	must(t, err)
	must(t, archive.Close())
	must(t, file.Close())
	snapshots := f.PreviewSnapshots()
	snapshot, err := snapshots.Open(ctx, scope, "file.docx", 20<<20)
	must(t, err)
	jobs := f.PreviewJobs(worker, t.TempDir())
	first, err := jobs.Open(ctx, scope, snapshot.ID, "docx")
	must(t, err)
	second, err := jobs.Open(ctx, scope, snapshot.ID, "docx")
	must(t, err)
	must(t, snapshots.Release(ctx, scope, snapshot.ID))
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := jobs.Status(ctx, scope, first.ID)
		must(t, err)
		if status.State == "failed" {
			t.Fatal(status)
		}
		if status.State == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	jobs.mu.Lock()
	count := len(jobs.jobs)
	jobs.mu.Unlock()
	if count != 1 {
		t.Fatal("duplicated conversion", count)
	}
	window, err := jobs.Window(ctx, scope, second.ID, 0, 1, 1)
	must(t, err)
	if len(window.Items) != 1 || window.Items[0].Text != "Final content" {
		t.Fatal(window)
	}
	position, err := jobs.Find(ctx, scope, first.ID, 0, 0, "Final")
	must(t, err)
	if position != 1 {
		t.Fatal(position)
	}
	text, err := jobs.Copy(ctx, scope, first.ID, 0, 0, 1, 0, 0)
	must(t, err)
	if text != "Initial content\nFinal content" {
		t.Fatal(text)
	}
	_, err = f.Store.CreateSession(ctx, scope, "shared-preview", "Shared preview")
	must(t, err)
	seedMessage(t, f.Store, scope, "shared-preview", "shared-message", "Preview the frozen document")
	shares := &Shares{Store: f.Store, Files: f, Directory: t.TempDir()}
	share, token, err := shares.Create(ctx, scope, "shared-preview", []string{"file.docx"}, time.Now().Add(time.Hour))
	must(t, err)
	public, err := jobs.OpenShared(ctx, shares, token, "file.docx", "docx")
	must(t, err)
	publicScope := Scope{Actor: "public:" + token, ProjectID: scope.ProjectID}
	for {
		status, e := jobs.Status(ctx, publicScope, public.ID)
		must(t, e)
		if status.State == "failed" {
			t.Fatal(status)
		}
		if status.State == "ready" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err = jobs.Window(ctx, publicScope, public.ID, 0, 0, 100)
	must(t, err)
	must(t, f.Store.RevokeShare(ctx, scope, share.ID))
	_, err = jobs.Window(ctx, publicScope, public.ID, 0, 0, 100)
	wantError(t, err, ErrNotFound)
	jobs.expire(public.ID)
	_, err = jobs.Status(ctx, Scope{Actor: "foreign", ProjectID: scope.ProjectID}, first.ID)
	wantError(t, err, ErrNotFound)
	must(t, jobs.Release(ctx, scope, first.ID))
	_, err = jobs.Status(ctx, scope, second.ID)
	must(t, err)
	must(t, jobs.Release(ctx, scope, second.ID))
	entries, err := os.ReadDir(jobs.Directory)
	must(t, err)
	if len(entries) != 0 {
		t.Fatal("derived artifacts leaked", entries)
	}
	if stats := jobs.Stats(); stats.Bytes != 0 || stats.Resident != 0 || stats.Running != 0 || stats.Leases != 0 {
		t.Fatal("leaked job quota", stats)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = jobs.Status(canceled, scope, second.ID)
	if err == nil {
		t.Fatal("canceled status accepted")
	}
}
