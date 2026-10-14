package history

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	manifest "aster/contracts/volumemanifest"
)

// Explicit opt-in keeps full-limit I/O out of ordinary unit-test runs.
func TestFullCapacity(t *testing.T) {
	if os.Getenv("ASTER_HISTORY_CAPACITY") != "1" {
		t.Skip("set ASTER_HISTORY_CAPACITY=1 for full-limit storage validation")
	}
	for _, profile := range []string{manifest.Profile, manifest.SharedAssetsProfile} {
		t.Run(profile, func(t *testing.T) {
			start := time.Now()
			ctx := context.Background()
			dir := t.TempDir()
			fileLimit, treeLimit, entryLimit := manifest.MaxFileBytes, manifest.MaxTreeBytes, manifest.MaxEntries
			if profile == manifest.SharedAssetsProfile {
				fileLimit, treeLimit, entryLimit = manifest.SharedAssetsMaxFileBytes, manifest.SharedAssetsMaxTreeBytes, manifest.SharedAssetsMaxEntries
			}
			s := Snapshot{Profile: profile}
			sources := map[string]string{}
			randomData := os.Getenv("ASTER_HISTORY_RANDOM") == "1"
			for i := int64(0); i < treeLimit/fileLimit; i++ {
				name := fmt.Sprintf("file-%d", i)
				source := filepath.Join(dir, "source-"+name)
				f, err := os.Create(source)
				if err != nil {
					t.Fatal(err)
				}
				h := sha256.New()
				var input io.Reader = zeroReader{}
				if randomData {
					input = rand.Reader
				}
				_, err = io.CopyN(io.MultiWriter(f, h), input, fileLimit)
				closeErr := f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
				sources[name] = source
				s.Entries = append(s.Entries, manifest.Entry{Kind: "file", Path: name, Size: fmt.Sprint(fileLimit), Digest: fmt.Sprintf("sha256:%x", h.Sum(nil))})
			}
			if profile == manifest.SharedAssetsProfile {
				s.Entries = append(s.Entries, fixture(map[string][]byte{"AGENTS.md": {}}).Entries...)
			}
			for len(s.Entries) < entryLimit {
				s.Entries = append(s.Entries, manifest.Entry{Kind: "directory", Path: fmt.Sprintf("empty-%05d", len(s.Entries)), Size: "0"})
			}
			repo, err := Open(ctx, filepath.Join(dir, "repo"))
			if err != nil {
				t.Fatal(err)
			}
			oid, err := repo.Commit(ctx, s, nil, "capacity", func(p string) (io.ReadCloser, error) {
				if p == "AGENTS.md" {
					return io.NopCloser(&zeroReader{remaining: 0, bounded: true}), nil
				}
				return os.Open(sources[p])
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CompareAndSwap(ctx, "refs/heads/main", "", oid); err != nil {
				t.Fatal(err)
			}
			got, err := repo.Snapshot(ctx, oid)
			if err != nil || got.TreeDigest() != s.TreeDigest() {
				t.Fatalf("full capacity round trip: %v", err)
			}
			over := Snapshot{Profile: profile, Entries: append([]manifest.Entry(nil), s.Entries...)}
			over.Entries = append(over.Entries, manifest.Entry{Kind: "directory", Path: "one-too-many", Size: "0"})
			if over.Validate() == nil {
				t.Fatal("entry limit not enforced")
			}
			over.Entries = append([]manifest.Entry(nil), s.Entries...)
			over.Entries[0].Size = fmt.Sprint(fileLimit + 1)
			if over.Validate() == nil {
				t.Fatal("file limit not enforced")
			}
			over.Entries = append([]manifest.Entry(nil), s.Entries...)
			over.Entries[len(over.Entries)-1] = fixture(map[string][]byte{"extra-byte": {1}}).Entries[0]
			if over.Validate() == nil {
				t.Fatal("tree limit not enforced")
			}
			if err := repo.Check(ctx); err != nil {
				t.Fatal(err)
			}
			var diskBytes int64
			err = filepath.WalkDir(repo.directory, func(p string, d os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if !d.IsDir() {
					info, e := d.Info()
					if e != nil {
						return e
					}
					diskBytes += info.Size()
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Admission rejects each over-limit tree before opening any bytes.
			for _, bad := range []Snapshot{over} {
				opened := false
				_, err := repo.Commit(ctx, bad, nil, "invalid", func(string) (io.ReadCloser, error) { opened = true; return io.NopCloser(strings.NewReader("")), nil })
				if err == nil || opened {
					t.Fatal("over-limit content reached object ingestion")
				}
			}
			t.Logf("incompressible=%t repository_bytes=%d", randomData, diskBytes)
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			t.Logf("profile=%s bytes=%d entries=%d elapsed=%s go=%s arch=%s heap_sys_at_end=%d", profile, treeLimit, entryLimit, time.Since(start), runtime.Version(), runtime.GOARCH, mem.HeapSys)
		})
	}
}

type zeroReader struct {
	remaining int64
	bounded   bool
}

func (z zeroReader) Read(p []byte) (int, error) {
	if z.bounded {
		return 0, io.EOF
	}
	clear(p)
	return len(p), nil
}
