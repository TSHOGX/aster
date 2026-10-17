package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRestoreRetainsProjectionAndRejectsTampering(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state := filepath.Join(root, "state", "projections.db")
	store, err := Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	config := Projection{OperationID: "op", Configuration: evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 10, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 1024}}}
	raw, _ := json.Marshal(config.Configuration)
	config.Digest = evolution.Digest(raw)
	if err = store.Adopt(ctx, config); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	if err = store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(backup, "backup.incomplete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete marker retained after success", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err = Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, filepath.Join(restored, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = opened.Read(ctx, "group", 1); err != nil {
		t.Fatal("restored projection missing", err)
	}
	opened.Close()
	manifestPath := filepath.Join(backup, "backup.json")
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest BackupManifest
	if err = json.Unmarshal(original, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Format = "evaluation-space-backup-v1"
	retired, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, retired, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, filepath.Join(root, "retired-format")); err == nil {
		t.Fatal("retired backup format accepted")
	}
	if err = os.WriteFile(manifestPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(backup, "projections.db"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, filepath.Join(root, "bad")); err == nil {
		t.Fatal("tampered backup accepted")
	}
}

func TestBackupRefusesStateDirectoryAndIncompleteRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state := filepath.Join(root, "state", "projections.db")
	store, err := Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Backup(ctx, filepath.Dir(state)); err == nil {
		t.Fatal("backup inside state directory accepted")
	}
	backup := filepath.Join(root, "incomplete")
	if err = os.MkdirAll(backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(backup, "backup.incomplete"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(ctx, backup, filepath.Join(root, "restored")); err == nil {
		t.Fatal("incomplete backup accepted")
	}
}

func TestInterruptedRestoreCannotStartAsEmptyInstallation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state", "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backup := filepath.Join(root, "backup")
	if err = store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = Restore(cancelled, backup, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore cancellation: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(target, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, filepath.Join(target, "projections.db"))
	if err == nil {
		opened.Close()
		t.Fatal("interrupted restore started as a fresh empty installation")
	}
	after, err := os.ReadFile(filepath.Join(target, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected startup changed partial restore")
	}
	if err = Restore(ctx, backup, target); err == nil {
		t.Fatal("partial destination overwritten")
	}
	if err = Restore(ctx, backup, filepath.Join(root, "retried")); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteBackupCannotStartAsInstallation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "backup.incomplete"), []byte("incomplete\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), filepath.Join(root, "projections.db"))
	if err == nil {
		store.Close()
		t.Fatal("incomplete backup started as a fresh installation")
	}
	if _, err := os.Stat(filepath.Join(root, "projections.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup created database inside incomplete backup", err)
	}
}

func TestRestoreRejectsInvalidSnapshotWithoutInitializingIt(t *testing.T) {
	for _, kind := range []string{"schema", "empty", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, err := Open(ctx, filepath.Join(root, "state", "projections.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if kind == "schema" {
				if _, err = store.db.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
					t.Fatal(err)
				}
			}
			backup := filepath.Join(root, "backup")
			if err = store.Backup(ctx, backup); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(backup, "projections.db")
			if kind == "empty" {
				if err = os.Remove(source); err != nil {
					t.Fatal(err)
				}
				empty, err := sql.Open("sqlite", ":memory:")
				if err != nil {
					t.Fatal(err)
				}
				_, err = empty.ExecContext(ctx, "VACUUM INTO ?", source)
				closeErr := empty.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			} else if kind == "corrupt" {
				if err = os.WriteFile(source, []byte("not a SQLite database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			size, digest, err := fileDigest(source)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(BackupManifest{Format: backupFormat, Files: []BackupFile{{Path: "projections.db", Size: size, Digest: digest}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(backup, "backup.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "restored")
			if err = Restore(ctx, backup, target); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if _, err = os.Stat(filepath.Join(target, "restore.incomplete")); err != nil {
				t.Fatal("failure lost startup barrier", err)
			}
			actualSize, actualDigest, err := fileDigest(filepath.Join(target, "projections.db"))
			if err != nil || actualSize != size || actualDigest != digest {
				t.Fatal("validation rewrote invalid snapshot", err)
			}
			if opened, err := Open(ctx, filepath.Join(target, "projections.db")); err == nil {
				opened.Close()
				t.Fatal("failed restore opened")
			}
		})
	}
}

func TestBackupSnapshotSerializesRetentionAndActiveRunPins(t *testing.T) {
	ctx := context.Background()
	s, _, lease, _ := journalFixture(t)
	pinned := retentionArtifact(t, s, "pinned-source", "active run input")
	if _, err := s.AppendRunEvent(ctx, lease, evolution.Event{ID: "source", RunID: lease.RunID, ProducerID: lease.WorkerID, Fence: lease.Fence, Timestamp: time.Now(), Kind: "artifact", Artifacts: []evolution.ArtifactRef{pinned}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for i := 0; i < 8; i++ {
		ref := retentionArtifact(t, s, fmt.Sprintf("retired-%d", i), "unreferenced evidence")
		input := ArtifactExpirationInput{RequestID: fmt.Sprintf("expiry-%d", i), Artifact: ref, Reason: "Retention during snapshot"}
		backup := filepath.Join(root, fmt.Sprintf("backup-%d", i))
		start := make(chan struct{})
		result := make(chan error, 3)
		go func() { <-start; result <- s.Backup(ctx, backup) }()
		go func() { <-start; _, err := s.ExpireArtifact(ctx, input); result <- err }()
		go func() {
			<-start
			raw, err := s.Artifact(ctx, pinned)
			if err == nil && !bytes.Equal(raw, []byte("active run input")) {
				err = fmt.Errorf("reader lost pinned bytes")
			}
			result <- err
		}()
		close(start)
		for n := 0; n < 3; n++ {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		}
		target := filepath.Join(root, fmt.Sprintf("restored-%d", i))
		if err := Restore(ctx, backup, target); err != nil {
			t.Fatal(err)
		}
		restored, err := Open(ctx, filepath.Join(target, "projections.db"))
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer restored.Close()
			raw, err := restored.Artifact(ctx, pinned)
			if err != nil || string(raw) != "active run input" {
				t.Fatal("restored active input lost", err)
			}
			if _, err = restored.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "deny-active", Artifact: pinned, Reason: "Must stay pinned"}); !errors.Is(err, ErrArtifactPinned) {
				t.Fatal("restore lost active run pin", err)
			}
			var used, actual int64
			if err = restored.db.QueryRowContext(ctx, "SELECT used_bytes,(SELECT coalesce(sum(length(body)),0) FROM artifacts) FROM artifact_store_quota WHERE id=1").Scan(&used, &actual); err != nil || used != actual {
				t.Fatal("snapshot split quota and bytes", used, actual, err)
			}
			// The snapshot may precede or follow expiry, but never split its body,
			// immutable receipt and capacity release across those two states.
			_, err = restored.Artifact(ctx, ref)
			var receipts int
			if queryErr := restored.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_expirations WHERE request_id=?", input.RequestID).Scan(&receipts); queryErr != nil {
				t.Fatal(queryErr)
			}
			if err == nil && receipts != 0 || errors.Is(err, ErrArtifactExpired) && receipts != 1 {
				t.Fatal("snapshot split expiry receipt and artifact", receipts, err)
			}
			if err != nil && !errors.Is(err, ErrArtifactExpired) {
				t.Fatal(err)
			}
		}()
	}
}

func TestBackupExcludesBrowserAuthorityWithoutLoggingOutSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state", "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seed := func(db *sql.DB) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "INSERT INTO app_sessions VALUES('token','csrf','issuer','subject','central',1,'private-grant',9999999999); INSERT INTO login_attempts VALUES('cookie','state','private-verifier','/',9999999999)"); err != nil {
			t.Fatal(err)
		}
	}
	count := func(db *sql.DB, want int) {
		t.Helper()
		var actual int
		if err := db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM app_sessions)+(SELECT count(*) FROM login_attempts)").Scan(&actual); err != nil || actual != want {
			t.Fatalf("browser authority count %d, want %d: %v", actual, want, err)
		}
	}
	seed(store.db)
	backup := filepath.Join(root, "backup")
	if err = store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	count(store.db, 2)
	snapshot, err := os.ReadFile(filepath.Join(backup, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snapshot, []byte("private-grant")) || bytes.Contains(snapshot, []byte("private-verifier")) {
		t.Fatal("removed browser secrets remain in snapshot bytes")
	}
	db, err := sql.Open("sqlite", filepath.Join(backup, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count(db, 0)
	// Restore also strips authority from an otherwise valid snapshot carrying
	// sessions; a backup's identity grants never authorize the restored instance.
	seed(db)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	size, digest, err := fileDigest(filepath.Join(backup, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(BackupManifest{Format: backupFormat, Files: []BackupFile{{Path: "projections.db", Size: size, Digest: digest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(backup, "backup.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored")
	if err = Restore(ctx, backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, filepath.Join(target, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	count(restored.db, 0)
	count(store.db, 2)
}
