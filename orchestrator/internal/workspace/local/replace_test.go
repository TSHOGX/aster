package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/workspace"
)

func directory(t *testing.T) Directory {
	t.Helper()
	return Directory{Path: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
}
func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReplaceCreatesDurableNestedFileAndDigest(t *testing.T) {
	d := directory(t)
	file, err := workspace.Write(context.Background(), d, "nested/path/file.txt", strings.NewReader("synthetic content"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(d.Path, file.Path)) != "synthetic content" || file.Size != 17 {
		t.Fatalf("unexpected file: %+v", file)
	}
	digest := sha256.Sum256([]byte("synthetic content"))
	if file.Digest != hex.EncodeToString(digest[:]) {
		t.Fatal("incorrect content digest")
	}
	info, err := os.Stat(filepath.Join(d.Path, file.Path))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("metadata: %v %v", info, err)
	}
}

func TestReplaceRefusesTraversalSymlinksAndSpecialFiles(t *testing.T) {
	d := directory(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d.Path, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(d.Path, "target")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d.Path, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/absolute", "../escape", "a/../escape", "a//b", "a\\b", "a\x00b", "link/target", "target", "directory"} {
		t.Run(path, func(t *testing.T) {
			if _, err := workspace.Write(context.Background(), d, path, strings.NewReader("replacement"), 100); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	if read(t, filepath.Join(outside, "target")) != "outside" {
		t.Fatal("outside file changed")
	}
}

func TestReplacePinnedParentSurvivesSymlinkSwap(t *testing.T) {
	d := directory(t)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(d.Path, "parent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "target"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := d.replace(context.Background(), "parent/target", strings.NewReader("replacement"), 100, func(stage string) error {
		if stage != "parent-open" {
			return nil
		}
		if err := os.Rename(filepath.Join(d.Path, "parent"), filepath.Join(d.Path, "pinned")); err != nil {
			return err
		}
		return os.Symlink(outside, filepath.Join(d.Path, "parent"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(outside, "target")) != "outside" || read(t, filepath.Join(d.Path, "pinned", "target")) != "replacement" {
		t.Fatal("followed swapped parent")
	}
}

func TestReplaceFailureBeforeRenamePreservesOldContentAndCleansStaging(t *testing.T) {
	for _, failure := range []string{"size", "reader", "sync", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			d := directory(t)
			target := filepath.Join(d.Path, "file")
			if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			var source io.Reader = strings.NewReader("new content")
			limit := int64(100)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "size" {
				limit = 2
			}
			if failure == "reader" {
				source = io.MultiReader(strings.NewReader("partial"), failedReader{})
			}
			_, err := d.replace(ctx, "file", source, limit, func(stage string) error {
				if stage == "file-synced" {
					if failure == "sync" {
						return errors.New("injected sync failure")
					}
					if failure == "canceled" {
						cancel()
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("injected failure succeeded")
			}
			if read(t, target) != "old" {
				t.Fatal("failed upload replaced old content")
			}
			entries, err := os.ReadDir(d.Path)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging leaked: %v %v", entries, err)
			}
		})
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

func TestReplaceReportsUncertainOutcomeAfterRename(t *testing.T) {
	d := directory(t)
	file, err := d.replace(context.Background(), "file", strings.NewReader("visible"), 100, func(stage string) error {
		if stage == "renamed" {
			return errors.New("injected directory sync failure")
		}
		return nil
	})
	if !errors.Is(err, workspace.ErrUncertain) {
		t.Fatalf("expected unresolved durability, got %v", err)
	}
	if file.Digest == "" || read(t, filepath.Join(d.Path, "file")) != "visible" {
		t.Fatal("lost reconciliation evidence")
	}
}
