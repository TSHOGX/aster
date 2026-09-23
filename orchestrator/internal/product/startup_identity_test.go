package product

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func activationDatabase(t *testing.T, version int, source, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "product.db")
	db := baselineDatabase(t, path)
	for _, step := range schemaSteps[1:version] {
		_, err := db.Exec(step)
		must(t, err)
	}
	_, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version))
	must(t, err)
	_, err = db.Exec(`UPDATE turns SET state='held'`)
	must(t, err)
	if version >= 10 {
		_, err = db.Exec(`UPDATE application_release SET phase='activating',current_instance='unchanged-source',activation_instance=?,target_digest=? WHERE id=1`, source, target)
	} else {
		_, err = db.Exec(`UPDATE application_release SET phase='activating' WHERE id=1`)
	}
	must(t, err)
	must(t, db.Close())
	return path
}

func TestRejectedReplacementCannotMigrateOrReplaceStoreIdentity(t *testing.T) {
	target := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name                   string
		version                int
		source, target, digest string
		rollback               bool
	}{
		{"wrong executable", 10, "source", target, other, false},
		{"missing source", 10, "", target, target, false},
		{"missing source in rollback", 10, "", target, other, true},
		{"invalid target", 10, "source", "invalid", other, true},
		{"unbound old activation", 9, "", "", other, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := activationDatabase(t, test.version, test.source, test.target)
			before, err := os.ReadFile(path)
			must(t, err)
			store, err := OpenApplication(ctx, path, ApplicationIdentity{ExecutableDigest: test.digest, RollbackCandidate: test.rollback})
			if err == nil {
				store.Close()
				t.Fatal("invalid replacement opened a writer")
			}
			after, err := os.ReadFile(path)
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected executable changed the database")
			}
			db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			must(t, err)
			defer db.Close()
			version, err := knownSchema(ctx, db)
			must(t, err)
			if version != test.version {
				t.Fatal("rejected executable migrated schema", version)
			}
			if version >= 10 {
				var instance string
				must(t, db.QueryRow(`SELECT current_instance FROM application_release WHERE id=1`).Scan(&instance))
				if instance != "unchanged-source" {
					t.Fatal("rejected executable claimed ownership", instance)
				}
			}
		})
	}
}

func TestExpectedOrExplicitRollbackExecutableMayUpgradeKnownSchema(t *testing.T) {
	target := "sha256:" + strings.Repeat("a", 64)
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint(rollback), func(t *testing.T) {
			path := activationDatabase(t, 10, "source", target)
			digest := target
			if rollback {
				digest = "sha256:" + strings.Repeat("b", 64)
			}
			store, err := OpenApplication(ctx, path, ApplicationIdentity{ExecutableDigest: digest, RollbackCandidate: rollback})
			must(t, err)
			defer store.Close()
			version, err := knownSchema(ctx, store.reader)
			must(t, err)
			if version != len(schemaSteps) {
				t.Fatal("accepted executable did not reach its schema", version)
			}
			state, err := store.ReleaseState(ctx)
			must(t, err)
			if state.Phase != "activating" || state.TargetDigest != target || state.ActivationInstance != "source" {
				t.Fatal("migration changed release authority", state)
			}
			turns, err := store.Turns(ctx, Scope{"owner", "p"}, "s", 0, 100)
			must(t, err)
			if len(turns) != 1 || turns[0].RequestID != "original-request" || turns[0].Content != "retained prompt" {
				t.Fatal("accepted writes lost", turns)
			}
		})
	}
}

func TestApplicationOpenRequiresMeasuredDigestBeforeCreatingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "product.db")
	if store, err := OpenApplication(ctx, path, ApplicationIdentity{}); err == nil {
		store.Close()
		t.Fatal("unmeasured executable accepted")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("invalid startup created state", err)
	}
}
