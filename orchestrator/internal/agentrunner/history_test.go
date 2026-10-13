package agentrunner

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryInstalledOnceAndVerified(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "sessions", "s", "native", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("original imported history\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	source := filepath.Join(root, ".history-s.jsonl")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c := Config{StateRoot: root, Session: "s", HistoryDigest: digest}
	if err := installHistory(c, native); err != nil {
		t.Fatal(err)
	}
	continued := []byte("original imported history\nnew conversation\n")
	if err := os.WriteFile(native, continued, 0600); err != nil {
		t.Fatal(err)
	}
	if err := installHistory(c, native); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(native)
	if err != nil || string(got) != string(continued) {
		t.Fatal(string(got), err)
	}
	if err = os.Remove(native); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = installHistory(c, native); err == nil {
		t.Fatal("unverified history installed")
	}
	if _, err = os.Stat(native); !os.IsNotExist(err) {
		t.Fatal("partial native history exposed", err)
	}
}
