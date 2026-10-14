package volumeimport

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"aster/contracts/volumemanifest"
	"harness-platform/orchestrator/internal/content"
)

func TestTreeIdentityMatchesRuntimeContent(t *testing.T) {
	for _, name := range []string{"empty-directory", "binary-and-empty", "unicode"} {
		b, err := os.ReadFile(filepath.Join("../../../volumes-studio/contracts/v1/fixtures", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		d, err := manifest.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]content.Entry, 0, len(d.Entries))
		for _, e := range d.Entries {
			n, _ := manifest.ParseSize(e.Size)
			entries = append(entries, content.Entry{Path: e.Path, Directory: e.Kind == "directory", Size: n, Digest: e.Digest})
		}
		if content.Digest(entries) != d.TreeDigest {
			t.Fatalf("%s: runtime content identity differs", name)
		}
	}
}
