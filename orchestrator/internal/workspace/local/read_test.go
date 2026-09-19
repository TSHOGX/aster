package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"harness-platform/orchestrator/internal/workspace"
)

func TestOpenSupportsRangesAndPinsTheOpenedFile(t *testing.T) {
	d := directory(t)
	if _, err := workspace.Write(context.Background(), d, "nested/file.txt", strings.NewReader("0123456789"), 100); err != nil {
		t.Fatal(err)
	}
	file, metadata, err := workspace.Open(context.Background(), d, "nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if metadata.Path != "nested/file.txt" || metadata.Size != 10 || metadata.ModifiedAt.IsZero() {
		t.Fatalf("metadata: %+v", metadata)
	}
	if _, err := file.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 4)
	if _, err := io.ReadFull(file, part); err != nil || string(part) != "3456" {
		t.Fatalf("range: %s %v", part, err)
	}
	if _, err := workspace.Write(context.Background(), d, "nested/file.txt", strings.NewReader("replacement"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	original, err := io.ReadAll(file)
	if err != nil || string(original) != "0123456789" {
		t.Fatalf("read followed replaced directory entry: %s %v", original, err)
	}
}

func TestOpenRejectsSymlinksAndSpecialFilesWithoutReadingThem(t *testing.T) {
	d := directory(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d.Path, "parent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(d.Path, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(d.Path, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d.Path, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parent/secret", "link", "fifo", "directory"} {
		t.Run(name, func(t *testing.T) {
			file, _, err := workspace.Open(context.Background(), d, name)
			if file != nil {
				file.Close()
			}
			if !errors.Is(err, workspace.ErrForbidden) {
				t.Fatalf("unsafe object opened: %v", err)
			}
		})
	}
}

func TestOpenReaderHonorsCancellation(t *testing.T) {
	d := directory(t)
	if _, err := workspace.Write(context.Background(), d, "file", strings.NewReader("content"), 100); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	file, _, err := workspace.Open(ctx, d, "file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cancel()
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read ignored cancellation: %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); !errors.Is(err, context.Canceled) {
		t.Fatalf("seek ignored cancellation: %v", err)
	}
}
