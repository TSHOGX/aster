package product

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func activationDatabase(t *testing.T, source, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "product.db")
	s, err := openFixtureStore(ctx, path)
	must(t, err)
	_, err = s.db.Exec(`UPDATE application_release SET phase='activating',current_instance='unchanged-source',activation_instance=?,target_digest=? WHERE id=1`, source, target)
	must(t, err)
	must(t, s.Close())
	return path
}
func TestRejectedReplacementCannotReplaceStoreIdentity(t *testing.T) {
	target := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct{ name, source, target, digest string }{{"wrong executable", "source", target, other}, {"missing source", "", target, target}, {"invalid target", "source", "invalid", target}} {
		t.Run(tc.name, func(t *testing.T) {
			path := activationDatabase(t, tc.source, tc.target)
			before, err := os.ReadFile(path)
			must(t, err)
			if s, err := OpenApplication(ctx, path, ApplicationIdentity{ExecutableDigest: tc.digest}); err == nil {
				s.Close()
				t.Fatal("invalid replacement acquired writer")
			}
			after, err := os.ReadFile(path)
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected candidate modified database")
			}
			db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			must(t, err)
			defer db.Close()
			var instance string
			must(t, db.QueryRow(`SELECT current_instance FROM application_release WHERE id=1`).Scan(&instance))
			if instance != "unchanged-source" {
				t.Fatal(instance)
			}
		})
	}
}
func TestSelectedExecutableReopensBehindActivationFence(t *testing.T) {
	target := "sha256:" + strings.Repeat("a", 64)
	path := activationDatabase(t, "source", target)
	s, err := OpenApplication(ctx, path, ApplicationIdentity{ExecutableDigest: target})
	must(t, err)
	defer s.Close()
	state, err := s.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "activating" || state.TargetDigest != target || state.ActivationInstance != "source" {
		t.Fatal(state)
	}
}
func TestApplicationOpenRequiresMeasuredDigestBeforeCreatingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "product.db")
	if s, err := OpenApplication(ctx, path, ApplicationIdentity{}); err == nil {
		s.Close()
		t.Fatal("unmeasured executable accepted")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("invalid startup created state", err)
	}
}
