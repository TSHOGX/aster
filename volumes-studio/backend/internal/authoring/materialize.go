package authoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	manifest "aster/contracts/volumemanifest"
)

// materialize holds the store mutex. A source writes into a bounded private
// temporary; only verified bytes become durable digest-addressed blobs.
func (s *Store) materialize(ctx context.Context, e manifest.Entry, write func(io.Writer) error) error {
	size, err := manifest.ParseSize(e.Size)
	if err != nil || e.Kind != "file" || !manifest.ValidDigest(e.Digest) || size > manifest.MaxFileBytes {
		return ErrInvalid
	}
	if s.poisoned {
		return ErrBusy
	}
	var retained int64
	err = s.db.QueryRowContext(ctx, "SELECT size FROM blobs WHERE digest=? AND state='live'", e.Digest).Scan(&retained)
	if err == nil {
		if retained != size {
			return ErrInvalid
		}
		return s.verifyFile(e.Digest, size)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = s.checkQuota(ctx, size*2); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Join(s.config.Directory, "tmp"), "history-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	bounded := &blobWriter{writer: io.MultiWriter(f, h), limit: size}
	if err = write(bounded); err == nil && (bounded.written != size || fmt.Sprintf("sha256:%x", h.Sum(nil)) != e.Digest) {
		err = fmt.Errorf("history blob integrity mismatch")
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Link(f.Name(), s.blobPath(e.Digest)); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err = s.verifyFile(e.Digest, size); err != nil {
		s.poisoned = true
		return err
	}
	if err = syncDir(filepath.Join(s.config.Directory, "blobs")); err != nil {
		s.poisoned = true
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO blobs(digest,size) VALUES(?,?)", e.Digest, size); err != nil {
		s.poisoned = true
		return err
	}
	return nil
}

type blobWriter struct {
	writer         io.Writer
	limit, written int64
}

func (w *blobWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.written {
		return 0, ErrInvalid
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}
