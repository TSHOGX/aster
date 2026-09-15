package deploymentgate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHoldPreventsNewBackgroundWork(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hold")
	t.Setenv("HARNESS_BACKGROUND_HOLD_FILE", p)
	t.Setenv("HARNESS_DRAIN_FILE", "")
	ran := 0
	Run(func() {
		ran++
		if Active() != 1 {
			t.Fatal("work not tracked")
		}
	})
	if ran != 1 || Active() != 0 {
		t.Fatal("active work leak")
	}
	os.WriteFile(p, []byte("op"), 0600)
	Run(func() { ran++ })
	if ran != 1 {
		t.Fatal("started work while held")
	}
	os.Remove(p)
	Run(func() { ran++ })
	if ran != 2 {
		t.Fatal("did not resume")
	}
}
func TestDrainFileErrorsFailClosed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file")
	os.WriteFile(p, []byte("not a directory"), 0600)
	t.Setenv("HARNESS_DRAIN_FILE", filepath.Join(p, "drain"))
	if !Draining() {
		t.Fatal("stat error did not close admission")
	}
}
