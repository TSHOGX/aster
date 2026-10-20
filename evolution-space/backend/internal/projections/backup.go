package projections

// Evolution Space backups are deliberately offline snapshots of the single
// projection database. Artifact bytes, run journals, evaluator submissions and
// immutable catalog history all live in that database. Copy it consistently,
// then remove only browser sessions and pending logins from the private snapshot.

import (
	"aster/contracts/appidentity"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const backupFormat = "evolution-space-backup-v1"

type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size,string"`
	Digest string `json:"digest"`
}

type BackupManifest struct {
	Format string       `json:"format"`
	Files  []BackupFile `json:"files"`
}

func (s *Store) Backup(ctx context.Context, target string) error {
	return s.BackupWith(ctx, target, nil)
}

// SnapshotFile writes one additional consistent database into a backup.
type SnapshotFile struct {
	Path  string
	Write func(context.Context, string) error
}

// BackupWith also includes companion databases, such as model connections, in
// the same manifest. Each is written to a fresh file and digested.
func (s *Store) BackupWith(ctx context.Context, target string, extra []SnapshotFile) error {
	if s == nil || s.db == nil || s.path == "" {
		return fmt.Errorf("projection store is not open")
	}
	if !filepath.IsAbs(target) || target == "/" {
		return fmt.Errorf("absolute backup destination required")
	}
	sourceDir := filepath.Dir(s.path)
	if rel, err := filepath.Rel(sourceDir, target); err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return fmt.Errorf("backup destination must be outside state directory")
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	marker := filepath.Join(target, "backup.incomplete")
	if err := os.WriteFile(marker, []byte("incomplete\n"), 0600); err != nil {
		return err
	}
	dbPath := filepath.Join(target, "projections.db")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dbPath); err != nil {
		return err
	}
	if err := os.Chmod(dbPath, 0600); err != nil {
		return err
	}
	if err := clearSnapshotSessions(ctx, dbPath); err != nil {
		return err
	}
	if err := syncFile(dbPath); err != nil {
		return err
	}
	size, digest, err := fileDigest(dbPath)
	if err != nil {
		return err
	}
	manifest := BackupManifest{Format: backupFormat, Files: []BackupFile{{Path: "projections.db", Size: size, Digest: digest}}}
	for _, file := range extra {
		if file.Path != "modelconn.db" {
			return fmt.Errorf("unsupported companion snapshot")
		}
		path := filepath.Join(target, file.Path)
		if err := file.Write(ctx, path); err != nil {
			return err
		}
		if err := syncFile(path); err != nil {
			return err
		}
		size, digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, BackupFile{Path: file.Path, Size: size, Digest: digest})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(target, "backup.json"), raw); err != nil {
		return err
	}
	if err := syncDir(target); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	if err := syncDir(target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

// Restore validates every manifest entry and checks the current schema without
// initializing or modifying the snapshot. The destination must be independent of
// both the source backup and any live state directory.
func Restore(ctx context.Context, source, target string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) || source == "/" || target == "/" || source == target {
		return fmt.Errorf("independent absolute restore paths required")
	}
	if rel, err := filepath.Rel(source, target); err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return fmt.Errorf("restore destination must be outside backup directory")
	}
	if _, err := os.Lstat(filepath.Join(source, "backup.incomplete")); err == nil {
		return fmt.Errorf("incomplete backup")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(source, "backup.json"))
	if err != nil {
		return err
	}
	var manifest BackupManifest
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.Format != backupFormat || len(manifest.Files) < 1 || len(manifest.Files) > 2 || manifest.Files[0].Path != "projections.db" || len(manifest.Files) == 2 && manifest.Files[1].Path != "modelconn.db" {
		return fmt.Errorf("invalid backup manifest")
	}
	var companion *BackupFile
	if len(manifest.Files) == 2 {
		companion = &manifest.Files[1]
		info, err := os.Stat(filepath.Join(source, companion.Path))
		if err != nil || !info.Mode().IsRegular() || info.Size() != companion.Size {
			return fmt.Errorf("backup model connection inventory mismatch")
		}
		if _, digest, err := fileDigest(filepath.Join(source, companion.Path)); err != nil || digest != companion.Digest {
			return fmt.Errorf("backup model connection digest mismatch")
		}
	}
	entry := manifest.Files[0]
	if entry.Size < 1 || entry.Digest == "" {
		return fmt.Errorf("invalid backup inventory")
	}
	sourceDB := filepath.Join(source, filepath.FromSlash(entry.Path))
	info, err := os.Stat(sourceDB)
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return fmt.Errorf("backup database inventory mismatch")
	}
	_, digest, err := fileDigest(sourceDB)
	if err != nil || digest != entry.Digest {
		return fmt.Errorf("backup database digest mismatch")
	}
	if err = os.Mkdir(target, 0700); err != nil {
		return err
	}
	// Retain a durable startup barrier on every failure, including cancellation
	// after an empty destination database was created. Such a file must never be
	// initialized as a fresh installation by ordinary startup.
	marker := filepath.Join(target, "restore.incomplete")
	if err = writePrivate(marker, []byte("incomplete\n")); err != nil {
		return err
	}
	if err = syncDir(target); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(target)); err != nil {
		return err
	}
	targetDB := filepath.Join(target, "projections.db")
	if err = copyPrivate(sourceDB, targetDB, ctx); err != nil {
		return err
	}
	// Verify the actual copied bytes too: verification of the source alone does
	// not bind a later read if the backup changes during the copy.
	size, digest, err := fileDigest(targetDB)
	if err != nil || size != entry.Size || digest != entry.Digest {
		return fmt.Errorf("restored database inventory mismatch")
	}
	if err = validateRestoredDatabase(ctx, targetDB); err != nil {
		return fmt.Errorf("restored projection schema rejected: %w", err)
	}
	if err = clearSnapshotSessions(ctx, targetDB); err != nil {
		return err
	}
	if companion != nil {
		targetCompanion := filepath.Join(target, companion.Path)
		if err = copyPrivate(filepath.Join(source, companion.Path), targetCompanion, ctx); err != nil {
			return err
		}
		if size, digest, err := fileDigest(targetCompanion); err != nil || size != companion.Size || digest != companion.Digest {
			return fmt.Errorf("restored model connection inventory mismatch")
		}
		if err = syncFile(targetCompanion); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = syncFile(targetDB); err != nil {
		return err
	}
	if err = syncDir(target); err != nil {
		return err
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	return syncDir(target)
}

func validateRestoredDatabase(ctx context.Context, path string) error {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	var integrity string
	err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
	if err == nil && integrity != "ok" {
		err = fmt.Errorf("restored database integrity check failed")
	}
	if err == nil {
		// A valid snapshot already has the exact current schema. Read-only mode
		// also prevents an empty SQLite database from becoming a fresh install.
		err = initializeSchema(ctx, db)
	}
	return errors.Join(err, db.Close())
}

func clearSnapshotSessions(ctx context.Context, path string) error {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_pragma=secure_delete(ON)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	err = appidentity.ClearSessions(ctx, db)
	if err == nil {
		// Compact the private copy so removed login grants and PKCE verifiers
		// are not retained in its free pages. The source database is untouched.
		_, err = db.ExecContext(ctx, "VACUUM")
	}
	return errors.Join(err, db.Close())
}

func fileDigest(path string) (int64, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	return n, "sha256:" + hex.EncodeToString(hash.Sum(nil)), errors.Join(copyErr, closeErr)
}

func copyPrivate(source, target string, ctx context.Context) error {
	in, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, copyErr := io.Copy(out, &contextReader{ctx: ctx, reader: in})
	if copyErr == nil {
		copyErr = out.Sync()
	}
	return errors.Join(copyErr, in.Close(), out.Close())
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func writePrivate(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncDir(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
