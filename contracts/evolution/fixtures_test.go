package evolution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSerializedWorkflowFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 4 {
		t.Fatal("missing protocol fixtures")
	}
	for _, name := range files {
		t.Run(filepath.Base(name), func(t *testing.T) {
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, err = DecodeWorkflow(f)
			valid := strings.HasSuffix(name, ".valid.json")
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && err == nil {
				t.Fatal("invalid serialized workflow accepted")
			}
		})
	}
}
