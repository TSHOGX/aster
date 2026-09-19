package prepared

import (
	"context"
	"errors"
	"harness-platform/orchestrator/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedUploadSurvivesReopenAndDetectsChangedContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := st.Prepare(ctx, strings.NewReader("prepared bytes"), 32)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	file, err := reopened.Open(ctx, content)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(data) != "prepared bytes" {
		t.Fatalf("prepared content: %q %v", data, err)
	}
	if _, err := st.Prepare(ctx, strings.NewReader("too large"), 1); !errors.Is(err, workspace.ErrTooLarge) {
		t.Fatalf("upload bound ignored: %v", err)
	}
	path := filepath.Join(root, content.Digest)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(ctx, content); err == nil {
		t.Fatal("changed prepared bytes accepted")
	}
	if _, err := st.Prepare(ctx, strings.NewReader("prepared bytes"), 32); err == nil {
		t.Fatal("corrupt existing digest reused")
	}
}

func TestPreparedUploadRejectsSymlinkAndCanceledPreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Prepare(ctx, strings.NewReader("body"), 8); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation persisted: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial preparation retained: %v %v", entries, err)
	}
	content, err := st.Prepare(context.Background(), strings.NewReader("body"), 8)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, content.Digest)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(context.Background(), content); err == nil {
		t.Fatal("prepared digest followed symlink")
	}
}
