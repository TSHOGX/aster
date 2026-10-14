package authoring

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
	"sort"
	"strings"
	"syscall"
)

// Backup is a local maintenance snapshot. Holding the owner mutex is an explicit
// backup pin over metadata, immutable blobs and upload checkpoints. It prevents
// references or GC claims from changing until all copied files are durable.
// App sessions intentionally require fresh central login after restore.
func (s *Store) Backup(ctx context.Context, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrBusy
	}
	if !filepath.IsAbs(target) || target == "/" {
		return ErrInvalid
	}
	relative, err := filepath.Rel(s.config.Directory, target)
	if err != nil {
		return err
	}
	if relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return ErrInvalid
	}
	if err = os.Mkdir(target, 0700); err != nil {
		return err
	}
	marker := filepath.Join(target, "backup.incomplete")
	if err = os.WriteFile(marker, []byte("incomplete\n"), 0600); err != nil {
		return err
	}
	if err = syncDir(target); err != nil {
		return err
	}
	for _, dir := range []string{"blobs", "uploads", "tmp"} {
		if err = os.Mkdir(filepath.Join(target, dir), 0700); err != nil {
			return err
		}
	}
	dbPath := filepath.Join(target, "authoring.db")
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO ?", dbPath); err != nil {
		return err
	}
	if err = os.Chmod(dbPath, 0600); err != nil {
		return err
	}
	file, err := os.OpenFile(dbPath, os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		return err
	}
	type record struct {
		Path   string `json:"path"`
		Size   int64  `json:"size,string"`
		Digest string `json:"digest"`
	}
	records := []record{}
	for _, dir := range []string{"blobs", "uploads"} {
		files, err := os.ReadDir(filepath.Join(s.config.Directory, dir))
		if err != nil {
			return err
		}
		for _, entry := range files {
			if err = ctx.Err(); err != nil {
				return err
			}
			if !entry.Type().IsRegular() {
				return ErrInvalid
			}
			rel := filepath.Join(dir, entry.Name())
			source, err := os.OpenFile(filepath.Join(s.config.Directory, rel), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			destination, err := os.OpenFile(filepath.Join(target, rel), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				source.Close()
				return err
			}
			hash := sha256.New()
			n, err := io.Copy(io.MultiWriter(destination, hash), &contextReader{ctx: ctx, reader: source})
			if err == nil {
				err = destination.Sync()
			}
			err = errors.Join(err, source.Close(), destination.Close())
			if err != nil {
				return err
			}
			digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
			if dir == "blobs" && digest != "sha256:"+entry.Name() {
				return fmt.Errorf("backup source integrity failed")
			}
			records = append(records, record{Path: filepath.ToSlash(rel), Size: n, Digest: digest})
		}
		if err = syncDir(filepath.Join(target, dir)); err != nil {
			return err
		}
	}
	historyRoot := filepath.Join(s.config.Directory, "history")
	if _, statErr := os.Stat(historyRoot); statErr == nil {
		err = filepath.WalkDir(historyRoot, func(name string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(s.config.Directory, name)
			if err != nil {
				return err
			}
			dest := filepath.Join(target, rel)
			if entry.IsDir() {
				return os.MkdirAll(dest, 0700)
			}
			if !entry.Type().IsRegular() {
				return ErrInvalid
			}
			source, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			output, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				source.Close()
				return err
			}
			hash := sha256.New()
			n, err := io.Copy(io.MultiWriter(output, hash), &contextReader{ctx: ctx, reader: source})
			if err == nil {
				err = output.Sync()
			}
			err = errors.Join(err, source.Close(), output.Close())
			if err != nil {
				return err
			}
			if err = syncDir(filepath.Dir(dest)); err != nil {
				return err
			}
			records = append(records, record{Path: filepath.ToSlash(rel), Size: n, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))})
			return nil
		})
		if err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	f, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &contextReader{ctx: ctx, reader: f})
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	records = append(records, record{Path: "authoring.db", Size: n, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))})
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	data, err := json.Marshal(struct {
		Format    string   `json:"format"`
		Publisher string   `json:"publisher_id"`
		Files     []record `json:"files"`
	}{"studio-backup-v1", s.config.PublisherID, records})
	if err != nil {
		return err
	}
	manifest, err := os.OpenFile(filepath.Join(target, "backup.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = manifest.Write(data); err == nil {
		err = manifest.Sync()
	}
	err = errors.Join(err, manifest.Close())
	if err != nil {
		return err
	}
	if err = syncDir(target); err != nil {
		return err
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	if err = syncDir(target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
