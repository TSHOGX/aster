package volume

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"io"
	"testing"
)

type leases struct {
	failClose    bool
	held, closed int
	reference    sandbox.Reference
}

func (l *leases) BeginOfflineWorkspaceRead(context.Context, string, string, string) (workspace.ReadLease, error) {
	l.held++
	return workspace.ReadLease{Reference: l.reference, Volume: sandbox.VolumeReference{StoreID: "store", VolumeID: "volume"}}, nil
}
func (l *leases) EndOfflineWorkspaceRead(context.Context, workspace.ReadLease) error {
	l.closed++
	if l.failClose {
		l.failClose = false
		return errors.New("lost lease release reply")
	}
	return nil
}

func TestOfflineReaderCannotReadAfterAmbiguousClose(t *testing.T) {
	ref := sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 2, MutationRevision: "paused"}
	repo := &leases{reference: ref, failClose: true}
	backend := &volumeBackend{reference: ref}
	file, _, err := (Files{Repository: repo, Backend: backend}).Open(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err == nil {
		t.Fatal("ambiguous lease release ignored")
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("reader continued after possibly released lease: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.reads != 0 || repo.closed != 2 {
		t.Fatal("lease cleanup did not retry independently of reader closure")
	}
}

type volumeBackend struct {
	sandbox.Lifecycle
	reference sandbox.Reference
	symlink   string
	reads     int
	short     bool
}

func (b *volumeBackend) Inspect(context.Context, sandbox.Reference) (sandbox.Observation, error) {
	return sandbox.Observation{Reference: b.reference, State: "paused", PlacementState: "lost"}, nil
}
func (b *volumeBackend) LstatVolume(_ context.Context, _ sandbox.VolumeReference, name string) (sandbox.VolumeEntry, error) {
	kind := "file"
	if name == "nested" {
		kind = "directory"
	}
	if name == b.symlink {
		kind = "symlink"
	}
	return sandbox.VolumeEntry{Name: name, Type: kind, Size: 6}, nil
}
func (b *volumeBackend) ReadVolume(_ context.Context, _ sandbox.VolumeReference, _ string, offset, length int64) ([]byte, error) {
	b.reads++
	data := []byte("abcdef")[offset : offset+length]
	if b.short {
		return data[:len(data)-1], nil
	}
	return data, nil
}
func TestOfflineVolumeReaderRejectsSymlinksAndReleasesLeaseOnError(t *testing.T) {
	for _, symlink := range []string{"nested", "nested/file"} {
		ref := sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 2, MutationRevision: "paused"}
		repo := &leases{reference: ref}
		backend := &volumeBackend{reference: ref, symlink: symlink}
		if _, _, err := (Files{Repository: repo, Backend: backend}).Open(context.Background(), "nested/file"); !errors.Is(err, workspace.ErrForbidden) {
			t.Fatalf("symlink path accepted: %v", err)
		}
		if repo.held != 1 || repo.closed != 1 || backend.reads != 0 {
			t.Fatalf("refusal leaked lease or read content: %+v %+v", repo, backend)
		}
	}
}
func TestOfflineVolumeReaderRangesAndShortResponse(t *testing.T) {
	ref := sandbox.Reference{StoreID: "store", SandboxID: "sandbox", Epoch: 2, MutationRevision: "paused"}
	repo := &leases{reference: ref}
	backend := &volumeBackend{reference: ref}
	file, info, err := (Files{Repository: repo, Backend: backend}).Open(context.Background(), "nested/file")
	if err != nil || info.Size != 6 {
		t.Fatalf("open: %+v %v", info, err)
	}
	if _, err := file.Seek(-3, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "def" {
		t.Fatalf("range: %q %v", data, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	backend.short = true
	if _, err := file.Read(make([]byte, 2)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short response hidden: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if repo.closed != 1 {
		t.Fatal("lease close not idempotent")
	}
	if _, err := file.Seek(0, io.SeekStart); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("closed reader still usable")
	}
}
