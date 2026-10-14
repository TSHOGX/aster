package volumeimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"aster/contracts/volumemanifest"
)

func hash(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func source(t *testing.T) (Identity, []byte) {
	t.Helper()
	d, err := manifest.New("publisher", "library", "version", []manifest.Entry{
		{Kind: "directory", Path: "empty-dir", Size: "0"},
		{Kind: "file", Path: "a", Size: "6", Digest: hash([]byte("abcdef"))},
		{Kind: "file", Path: "b", Size: "6", Digest: hash([]byte("abcdef"))},
		{Kind: "file", Path: "empty", Size: "0", Digest: hash(nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := d.Canonical()
	digest, _ := d.Digest()
	return Identity{ID: "import", SourceID: "source", PublisherID: d.PublisherID, VersionID: d.VersionID, DescriptorDigest: digest, Profile: manifest.Profile}, raw
}
func admit(t *testing.T) (*Cache, string, Identity, []byte) {
	t.Helper()
	id, raw := source(t)
	root := filepath.Join(t.TempDir(), "cache")
	c, err := Admit(root, id, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, root, id, raw
}
func appendChunk(t *testing.T, c *Cache, digest string, offset int64, data []byte) {
	t.Helper()
	if err := c.Append(digest, offset, data, hash(data)); err != nil {
		t.Fatal(err)
	}
}
func TestDurableResumeAndOfflineReads(t *testing.T) {
	c, root, id, raw := admit(t)
	digest := hash([]byte("abcdef"))
	if len(c.Status().Blobs) != 2 {
		t.Fatal("duplicate blobs not deduplicated")
	}
	appendChunk(t, c, digest, 0, []byte("abc"))
	if _, err := c.ReadFile("a"); !errors.Is(err, ErrCheckpoint) {
		t.Fatal("partial source readable", err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("second owner admitted")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// A killed writer can leave unacknowledged bytes; recovery discards them.
	f, err := os.OpenFile(filepath.Join(root, digest[7:]+".blob"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte("tail"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	c, err = Admit(root, id, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	info, err := os.Stat(filepath.Join(root, digest[7:]+".blob"))
	if err != nil || info.Size() != 3 {
		t.Fatal("tail not reconciled", err)
	}
	appendChunk(t, c, digest, 0, []byte("abc")) // lost acknowledgment retry
	if err := c.Append(digest, 0, []byte("xxx"), hash([]byte("xxx"))); !errors.Is(err, ErrIdentityConflict) {
		t.Fatal(err)
	}
	appendChunk(t, c, digest, 3, []byte("def"))
	if c.Status().State == SourceAcquired {
		t.Fatal("unverified empty file accepted")
	}
	appendChunk(t, c, hash(nil), 0, nil)
	if c.Status().State != SourceAcquired {
		t.Fatal(c.Status())
	}
	c.Close()
	c, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, name := range []string{"a", "b", "empty"} {
		f, err := c.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if name != "empty" && string(data) != "abcdef" {
			t.Fatal(string(data))
		}
	}
	if _, err = c.ReadFile("../a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
func TestDescriptorIdentityAndConflictingAdmission(t *testing.T) {
	c, root, id, raw := admit(t)
	c.Close()
	for _, mutate := range []func(*Identity){func(i *Identity) { i.SourceID = "different" }, func(i *Identity) { i.PublisherID = "other" }, func(i *Identity) { i.VersionID = "other" }, func(i *Identity) { i.Profile = "system" }, func(i *Identity) { i.DescriptorDigest = hash(nil) }} {
		changed := id
		mutate(&changed)
		if reopened, err := Admit(root, changed, raw); err == nil {
			reopened.Close()
			t.Fatal("identity rebound")
		}
	}
	if reopened, err := Admit(root, id, append(raw, '\n')); err == nil {
		reopened.Close()
		t.Fatal("noncanonical descriptor admitted")
	}
}
func TestCorruptBlobQuarantinesAndCannotBecomeAcquired(t *testing.T) {
	c, root, _, _ := admit(t)
	digest := hash([]byte("abcdef"))
	if err := c.Append(digest, 0, []byte("broken"), hash([]byte("broken"))); !errors.Is(err, ErrIdentityConflict) {
		t.Fatal(err)
	}
	if c.Status().State != Quarantined {
		t.Fatal(c.Status())
	}
	if err := c.Append(digest, 0, []byte("abcdef"), digest); !errors.Is(err, ErrQuarantined) {
		t.Fatal(err)
	}
	c.Close()
	c, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Status().State != Quarantined {
		t.Fatal(c.Status())
	}
}
func TestReopenRejectsMissingCorruptOrForgedAcquiredState(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "forged"} {
		t.Run(mode, func(t *testing.T) {
			c, root, _, _ := admit(t)
			digest := hash([]byte("abcdef"))
			if mode != "forged" {
				appendChunk(t, c, digest, 0, []byte("abcdef"))
				appendChunk(t, c, hash(nil), 0, nil)
			}
			c.Close()
			switch mode {
			case "missing":
				if err := os.Remove(filepath.Join(root, digest[7:]+".blob")); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filepath.Join(root, digest[7:]+".blob"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "forged":
				b, err := os.ReadFile(filepath.Join(root, "job.json"))
				if err != nil {
					t.Fatal(err)
				}
				var j Job
				if err = json.Unmarshal(b, &j); err != nil {
					t.Fatal(err)
				}
				j.State = SourceAcquired
				b, _ = json.Marshal(j)
				if err = os.WriteFile(filepath.Join(root, "job.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if c, err := Open(root); err == nil {
				c.Close()
				t.Fatal("unverified state reopened")
			}
		})
	}
}
func TestCheckpointsAreOwnedByDurableBytes(t *testing.T) {
	c, _, _, _ := admit(t)
	d := hash([]byte("abcdef"))
	for _, v := range []struct {
		digest string
		offset int64
		data   []byte
		hash   string
	}{
		{d, 1, []byte("a"), hash([]byte("a"))}, {d, -1, nil, hash(nil)}, {hash([]byte("unknown")), 0, nil, hash(nil)}, {d, 0, []byte("abcdefg"), hash([]byte("abcdefg"))}, {d, 0, []byte("a"), hash(nil)}, {d, 0, nil, hash(nil)},
	} {
		if err := c.Append(v.digest, v.offset, v.data, v.hash); err == nil {
			t.Fatal("invalid checkpoint accepted")
		}
	}
	// Status returns a copy; callers cannot bypass verification by mutating it.
	j := c.Status()
	j.State = SourceAcquired
	j.Blobs[0].Verified = true
	if c.Status().State != Acquiring || c.Status().Blobs[0].Verified {
		t.Fatal("mutable authority escaped")
	}
}
func TestVerifyBlobRejectsLengthDigestAndReadErrors(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 1024)
	digest := hash(data)
	if n, err := VerifyBlob(bytes.NewReader(data), digest, 1024); err != nil || n != 1024 {
		t.Fatal(n, err)
	}
	for _, v := range []struct {
		data   []byte
		digest string
		size   int64
	}{{append(data, 1), digest, 1024}, {data[:1023], digest, 1024}, {data, hash(nil), 1024}, {data, digest, -1}, {data, digest, manifest.MaxFileBytes + 1}} {
		if _, err := VerifyBlob(bytes.NewReader(v.data), v.digest, v.size); err == nil {
			t.Fatal("invalid blob accepted")
		}
	}
	if _, err := VerifyBlob(io.MultiReader(bytes.NewReader(data), brokenReader{}), digest, 1024); err == nil {
		t.Fatal("read error at boundary ignored")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestDirectoryOnlyAcquisition(t *testing.T) {
	d, err := manifest.New("publisher", "library", "version", []manifest.Entry{{Kind: "directory", Path: "empty", Size: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := d.Canonical()
	digest, _ := d.Digest()
	id := Identity{ID: "i", SourceID: "s", PublisherID: "publisher", VersionID: "version", DescriptorDigest: digest, Profile: manifest.Profile}
	c, err := Admit(filepath.Join(t.TempDir(), "cache"), id, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Status().State != SourceAcquired {
		t.Fatal(c.Status())
	}
}
