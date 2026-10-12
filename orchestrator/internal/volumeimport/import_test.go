package volumeimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"harness-platform/orchestrator/internal/volumeimport/manifest"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) ([]byte, string) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "volumes-studio", "contracts", "v1", "fixtures")
	b, e := os.ReadFile(filepath.Join(root, "binary-and-empty.json"))
	if e != nil {
		t.Fatal(e)
	}
	var rows []struct {
		Name   string `json:"name"`
		Digest string `json:"descriptor_digest"`
	}
	idx, e := os.ReadFile(filepath.Join(root, "index.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(idx, &rows); e != nil {
		t.Fatal(e)
	}
	for _, r := range rows {
		if r.Name == "binary-and-empty" {
			return b, r.Digest
		}
	}
	t.Fatal("fixture digest")
	return nil, ""
}
func TestAcquireCheckpointResumeAndIdentity(t *testing.T) {
	raw, digest := fixture(t)
	s, e := New("import-1", "source-1", "version-1", digest, manifest.Profile)
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.AdmitManifest(raw)
	if e != nil {
		t.Fatal(e)
	}
	if s.Job.State != Acquiring || len(d.Entries) != 4 {
		t.Fatal(s.Job)
	}
	var file manifest.Entry
	for _, x := range d.Entries {
		if x.Kind == "file" {
			file = x
			break
		}
	}
	n, _ := manifest.ParseSize(file.Size)
	if e = s.Checkpoint(file.Digest, n+1); e != ErrCheckpoint {
		t.Fatal(e)
	}
	if e = s.Checkpoint(file.Digest, n); e != nil {
		t.Fatal(e)
	}
	for _, x := range s.Job.Blobs {
		if x.Digest != file.Digest {
			size, _ := manifest.ParseSize(x.Size)
			if e = s.Checkpoint(x.Digest, size); e != nil {
				t.Fatal(e)
			}
		}
	}
	if !s.Ready() {
		t.Fatal(s.Job)
	}
	s.Quarantine()
	if s.Ready() {
		t.Fatal("quarantine ready")
	}
}
func TestVerifyBlobStreaming(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 1024)
	sum := "sha256:" + hash(data)
	if n, e := VerifyBlob(bytes.NewReader(data), sum, int64(len(data))); e != nil || n != 1024 {
		t.Fatal(n, e)
	}
	if _, e := VerifyBlob(bytes.NewReader(append(data, 1)), sum, 1024); e == nil {
		t.Fatal("extra bytes accepted")
	}
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
