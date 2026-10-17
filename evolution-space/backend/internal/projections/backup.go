package projections

// Evolution Space backups are deliberately offline snapshots of the single
// projection database. Artifact bytes, run journals, evaluator submissions and
// immutable catalog history all live in that database, so copying only selected
// tables would create an authorization and ancestry hole.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if err := syncFile(dbPath); err != nil {
		return err
	}
	size, digest, err := fileDigest(dbPath)
	if err != nil {
		return err
	}
	manifest := BackupManifest{Format: backupFormat, Files: []BackupFile{{Path: "projections.db", Size: size, Digest: digest}}}
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
	return syncDir(filepath.Dir(target))
}

// Restore validates every manifest entry and opens the restored database with
// the current schema before returning. The destination must be independent of
// both the source backup and any live state directory.
func Restore(ctx context.Context, source, target string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) || source == "/" || target == "/" || source == target {
		return fmt.Errorf("independent absolute restore paths required")
	}
	if _, err := os.Stat(filepath.Join(source, "backup.incomplete")); err == nil {
		return fmt.Errorf("incomplete backup")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(source, "backup.json"))
	if err != nil {
		return err
	}
	var manifest BackupManifest
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.Format != backupFormat || len(manifest.Files) != 1 || manifest.Files[0].Path != "projections.db" {
		return fmt.Errorf("invalid backup manifest")
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
	defer func() {
		if _, statErr := os.Stat(filepath.Join(target, "projections.db")); statErr != nil {
			_ = os.RemoveAll(target)
		}
	}()
	targetDB := filepath.Join(target, "projections.db")
	if err = copyPrivate(sourceDB, targetDB, ctx); err != nil {
		return err
	}
	check, err := Open(ctx, targetDB)
	if err != nil {
		return fmt.Errorf("restored projection schema rejected: %w", err)
	}
	return check.Close()
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
