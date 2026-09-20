package trajectory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"harness-platform/orchestrator/internal/nativecapture"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeArchiveIntegrityAndRetry(t *testing.T) {
	root := t.TempDir()
	capture := NewCapturer(root)
	raw := "{\"type\":\"session\",\"id\":\"native\",\"cwd\":\"/workspace\"}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	request := nativecapture.Request{SessionID: "session", NativeID: "native", Size: int64(len(raw)), NativeDigest: digest}
	first, err := capture.CaptureNativeSnapshot(context.Background(), request, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.CaptureNativeSnapshot(context.Background(), request, strings.NewReader(raw))
	if err != nil || second.ArchivePath != first.ArchivePath {
		t.Fatalf("retry: %+v %v", second, err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "raw", "session", "*.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("archives: %v %v", paths, err)
	}
	data, err := os.ReadFile(first.ArchivePath)
	if err != nil || string(data) != raw {
		t.Fatalf("native bytes: %v", err)
	}
	if _, err := capture.CaptureNativeSnapshot(context.Background(), request, strings.NewReader(raw+"extra")); err == nil {
		t.Fatal("accepted size/digest mismatch")
	}
	request.NativeID = "another"
	if _, err := capture.CaptureNativeSnapshot(context.Background(), request, strings.NewReader(raw)); err == nil {
		t.Fatal("accepted different native identity")
	}
	request.NativeID = "native"
	os.Chmod(first.ArchivePath, 0644)
	os.WriteFile(first.ArchivePath, []byte("corrupt"), 0444)
	if _, err := capture.CaptureNativeSnapshot(context.Background(), request, strings.NewReader(raw)); err == nil {
		t.Fatal("accepted corrupted existing archive")
	}
}
