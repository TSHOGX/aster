package product

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceReaderReusesHelperWithoutReadLedgerWrites(t *testing.T) {
	f, scope, backend := fileFixture(t)
	f.CaptureDirectory = t.TempDir()
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("original"), 0600))
	capture, err := f.Capture(ctx, scope, "file")
	must(t, err)
	hash := capture.Digest
	data, err := io.ReadAll(capture)
	must(t, err)
	must(t, capture.Close())
	if string(data) != "original" {
		t.Fatal(string(data))
	}
	entries, err := os.ReadDir(f.CaptureDirectory)
	must(t, err)
	if len(entries) != 0 {
		t.Fatal("capture left a named temporary file")
	}
	var changesBefore, changesAfter int
	must(t, f.Store.db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore))
	for n := 0; n < 50; n++ {
		page, e := f.List(ctx, scope, "", "", 100)
		must(t, e)
		if len(page.Entries) != 1 {
			t.Fatal(page)
		}
	}
	must(t, f.Store.db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter))
	if changesBefore != changesAfter {
		t.Fatal("warm reads wrote database", changesBefore, changesAfter)
	}
	backend.mu.Lock()
	starts := backend.starts
	backend.mu.Unlock()
	if starts != 1 {
		t.Fatal("per-read helpers", starts)
	}
	var rows, activity int
	must(t, f.Store.reader.QueryRow(`SELECT count(*) FROM file_operations`).Scan(&rows))
	must(t, f.Store.reader.QueryRow(`SELECT count(*) FROM project_activity WHERE kind='files'`).Scan(&activity))
	if rows != 0 || activity != 0 {
		t.Fatal(rows, activity)
	}
	info, err := os.Stat(filepath.Join(backend.root, "file"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("changed!"), 0600))
	must(t, os.Chtimes(filepath.Join(backend.root, "file"), info.ModTime(), info.ModTime()))
	newer, err := f.Capture(ctx, scope, "file")
	must(t, err)
	defer newer.Close()
	if newer.Digest == hash {
		t.Fatal("metadata-only validator")
	}
	// Returned capture remains immutable while the original changes again.
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("now live"), 0600))
	data, err = io.ReadAll(newer)
	must(t, err)
	if string(data) != "changed!" {
		t.Fatal(string(data))
	}
}

func TestWorkspaceReaderScopedSnapshotAndLifecycle(t *testing.T) {
	f, scope, backend := fileFixture(t)
	for n := 0; n < 5; n++ {
		must(t, os.WriteFile(filepath.Join(backend.root, fmt.Sprint(n)), nil, 0600))
	}
	page, err := f.List(ctx, scope, "", "", 2)
	must(t, err)
	if page.Next == "" {
		t.Fatal("missing continuation")
	}
	_, err = f.List(ctx, Scope{Actor: "foreign", ProjectID: scope.ProjectID}, "", page.Next, 2)
	wantError(t, err, ErrNotFound)
	_, err = f.List(ctx, scope, "other", page.Next, 2)
	wantError(t, err, ErrInvalid)
	must(t, os.Remove(filepath.Join(backend.root, "2")))
	next, err := f.List(ctx, scope, "", page.Next, 2)
	must(t, err)
	if next.Entries[0].Name != "2" {
		t.Fatal(next)
	}
	capture, err := f.Capture(ctx, scope, "0")
	must(t, err)
	defer capture.Close()
	_, err = f.Store.ArmIdle(ctx, scope, time.Minute)
	must(t, err)
	project, err := f.Store.Project(ctx, scope)
	must(t, err)
	must(t, f.Lifecycle.Pause(ctx, scope, project.Revision))
	drive(t, f.Lifecycle, scope.ProjectID)
	deadline := time.Now().Add(2 * time.Second)
	for capture.ctx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = capture.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal("old capture survived placement pause", err)
	}
	_, err = f.List(ctx, scope, "", "", 2)
	wantError(t, err, ErrPending)
	p, err := f.Store.Project(ctx, scope)
	must(t, err)
	if p.State != "paused" {
		t.Fatal("listing woke project")
	}
	_, err = f.Capture(ctx, scope, "0")
	wantError(t, err, ErrPending)
	drive(t, f.Lifecycle, scope.ProjectID)
	_, err = f.List(ctx, scope, "", page.Next, 2)
	wantError(t, err, ErrResync)
}

func TestWorkspaceReaderCanceledConsumerAndCrashBinding(t *testing.T) {
	f, scope, backend := fileFixture(t)
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte(strings.Repeat("x", 1<<20)), 0600))
	firstCtx, cancel := context.WithCancel(ctx)
	first, err := f.Capture(firstCtx, scope, "file")
	must(t, err)
	defer first.Close()
	second, err := f.Capture(ctx, scope, "file")
	must(t, err)
	defer second.Close()
	cancel()
	if _, err = first.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	data, err := io.ReadAll(second)
	must(t, err)
	if len(data) != 1<<20 {
		t.Fatal(len(data))
	}
	first.Close()
	second.Close()
	original, err := f.workspaceReader().saved(ctx, scope.ProjectID)
	must(t, err)
	// Simulate a lost host carrier while retaining its durable helper binding.
	f.reader.mu.Lock()
	entry := f.reader.entries[scope.ProjectID]
	entry.cancel()
	delete(f.reader.entries, scope.ProjectID)
	f.reader.mu.Unlock()
	f.reader.group.Wait()
	recovered, err := f.Capture(ctx, scope, "file")
	must(t, err)
	recovered.Close()
	current, err := f.reader.saved(ctx, scope.ProjectID)
	must(t, err)
	if current.Handle == original.Handle {
		t.Fatal("abandoned stream identity reused")
	}
	var count int
	must(t, f.Store.reader.QueryRow(`SELECT count(*) FROM workspace_readers WHERE project_id=?`, scope.ProjectID).Scan(&count))
	if count != 1 {
		t.Fatal(count)
	}
	readers, requests, bytes := f.reader.Stats()
	if readers != 1 || requests != 0 || bytes != 0 {
		t.Fatal(readers, requests, bytes)
	}
}
