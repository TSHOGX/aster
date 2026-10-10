package product

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreviewSnapshotRangesReuseCaptureAndKeepVersion(t *testing.T) {
	f, scope, backend := fileFixture(t)
	source := filepath.Join(backend.root, "file")
	must(t, os.WriteFile(source, []byte("abcdefghij"), 0600))
	snapshots := f.PreviewSnapshots()
	descriptor, err := snapshots.Open(ctx, scope, "file", 20<<20)
	must(t, err)
	if stats := f.ReadStats(); stats.Leases != 0 || stats.CapturedBytes != 0 {
		t.Fatal("capture retained runtime activity", stats)
	}
	must(t, os.WriteFile(source, []byte("ABCDEFGHIJ"), 0600))
	for n := 0; n < 10; n++ {
		got, reader, release, err := snapshots.Acquire(ctx, scope, descriptor.ID)
		must(t, err)
		if got.Digest != descriptor.Digest {
			t.Fatal("spliced versions")
		}
		_, err = reader.Seek(int64(n), io.SeekStart)
		must(t, err)
		data := make([]byte, 1)
		_, err = io.ReadFull(reader, data)
		must(t, err)
		release()
		release()
		if data[0] != byte('a'+n) {
			t.Fatal(string(data))
		}
	}
	// Each refresh captures fresh bytes, including changes with identical metadata.
	newer, err := snapshots.Open(ctx, scope, "file", 20<<20)
	must(t, err)
	if newer.Digest == descriptor.Digest {
		t.Fatal("refresh returned stale bytes")
	}
	_, _, _, err = snapshots.Acquire(ctx, Scope{Actor: "foreign", ProjectID: scope.ProjectID}, descriptor.ID)
	wantError(t, err, ErrNotFound)
	must(t, snapshots.Release(ctx, scope, descriptor.ID))
	_, _, _, err = snapshots.Acquire(ctx, scope, descriptor.ID)
	wantError(t, err, ErrSnapshotExpired)
	must(t, snapshots.Release(ctx, scope, newer.ID))
	snapshots.mu.Lock()
	bytes := snapshots.bytes
	snapshots.mu.Unlock()
	if bytes != 0 {
		t.Fatal("leaked capture", bytes)
	}
}
func TestPreviewSnapshotAdmissionExpiryAndActiveRangeCleanup(t *testing.T) {
	f, scope, backend := fileFixture(t)
	source, err := os.Create(filepath.Join(backend.root, "large"))
	must(t, err)
	must(t, source.Truncate(21<<20))
	must(t, source.Close())
	snapshots := f.PreviewSnapshots()
	_, err = snapshots.Open(ctx, scope, "large", 20<<20)
	wantError(t, err, ErrPreviewSourceTooLarge)
	if stats := f.ReadStats(); stats.Leases != 0 || stats.CapturedBytes != 0 {
		t.Fatal(stats)
	}
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("retained"), 0600))
	descriptor, err := snapshots.Open(ctx, scope, "file", 20<<20)
	must(t, err)
	_, reader, release, err := snapshots.Acquire(ctx, scope, descriptor.ID)
	must(t, err)
	snapshots.expire(descriptor.ID)
	_, _, _, err = snapshots.Acquire(ctx, scope, descriptor.ID)
	wantError(t, err, ErrSnapshotExpired)
	data, err := io.ReadAll(reader)
	must(t, err)
	if string(data) != "retained" {
		t.Fatal(string(data))
	}
	release()
	snapshots.mu.Lock()
	bytes := snapshots.bytes
	snapshots.mu.Unlock()
	if bytes != 0 {
		t.Fatal(bytes)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = snapshots.Open(canceled, scope, "file", 20<<20)
	if err == nil {
		t.Fatal("canceled consumer opened lease")
	}
	snapshots.Close()
	_, err = snapshots.Open(ctx, scope, "file", 20<<20)
	wantError(t, err, ErrCapacity)
}
func TestPreviewSnapshotSurvivesPauseWithoutResumingProject(t *testing.T) {
	f, scope, backend := fileFixture(t)
	must(t, os.WriteFile(filepath.Join(backend.root, "file"), []byte("immutable"), 0600))
	snapshots := f.PreviewSnapshots()
	descriptor, err := snapshots.Open(ctx, scope, "file", 20<<20)
	must(t, err)
	p, err := f.Store.Project(ctx, scope)
	must(t, err)
	must(t, f.Lifecycle.Pause(ctx, scope, p.Revision))
	drive(t, f.Lifecycle, scope.ProjectID)
	_, reader, release, err := snapshots.Acquire(ctx, scope, descriptor.ID)
	must(t, err)
	data, err := io.ReadAll(reader)
	must(t, err)
	release()
	if string(data) != "immutable" {
		t.Fatal(string(data))
	}
	paused, err := snapshots.Open(ctx, scope, "file", 20<<20)
	must(t, err)
	must(t, snapshots.Release(ctx, scope, paused.ID))
	p, err = f.Store.Project(ctx, scope)
	must(t, err)
	if p.State != "paused" {
		t.Fatal("preview resumed compute", p.State)
	}
	snapshots.mu.Lock()
	snapshots.leases[descriptor.ID].Expires = time.Now().Add(-time.Second)
	snapshots.mu.Unlock()
	_, _, _, err = snapshots.Acquire(ctx, scope, descriptor.ID)
	wantError(t, err, ErrSnapshotExpired)
}
