package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"harness-platform/orchestrator/internal/nativecapture"
)

type CaptureIdentity struct{ SessionID, NativeSessionID string }

// CaptureNativeSnapshot archives and verifies the exact committed native bytes.
func (c *Capturer) CaptureNativeSnapshot(ctx context.Context, p nativecapture.Request, source io.Reader) (nativecapture.Result, error) {
	if c == nil || p.Size <= 0 || p.Size > 512<<20 || len(p.NativeDigest) != 64 {
		return nativecapture.Result{}, fmt.Errorf("enabled capturer and bounded native snapshot evidence required")
	}
	return c.captureStream(ctx, CaptureIdentity{SessionID: p.SessionID, NativeSessionID: p.NativeID}, io.LimitReader(source, p.Size+1), p.NativeDigest, p.Size)
}

func (c *Capturer) captureStream(ctx context.Context, identity CaptureIdentity, source io.Reader, expectedDigest string, expectedSize int64) (nativecapture.Result, error) {
	if identity.SessionID == "" || sanitizeName(identity.SessionID) != identity.SessionID {
		return nativecapture.Result{}, fmt.Errorf("safe capture session identity required")
	}
	directory := filepath.Join(c.archiveRoot, identity.SessionID)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nativecapture.Result{}, err
	}
	if err := syncArchiveDirectory(c.archiveRoot); err != nil {
		return nativecapture.Result{}, err
	}
	staging, err := os.CreateTemp(directory, ".capture-*")
	if err != nil {
		return nativecapture.Result{}, err
	}
	defer staging.Close()
	defer os.Remove(staging.Name())
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(staging, hash), captureReader{ctx: ctx, source: source})
	if err != nil {
		return nativecapture.Result{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedDigest != "" && (digest != expectedDigest || size != expectedSize) {
		return nativecapture.Result{}, fmt.Errorf("captured native snapshot does not match committed size/digest")
	}
	if err := staging.Chmod(0444); err != nil {
		return nativecapture.Result{}, err
	}
	if err := staging.Sync(); err != nil {
		return nativecapture.Result{}, err
	}
	if _, err := staging.Seek(0, io.SeekStart); err != nil {
		return nativecapture.Result{}, err
	}
	_, meta, err := ParsePiSession(staging)
	if err != nil {
		return nativecapture.Result{}, fmt.Errorf("parse captured native source: %w", err)
	}
	if identity.NativeSessionID != "" && identity.NativeSessionID != meta.NativeSessionID {
		return nativecapture.Result{}, fmt.Errorf("captured native session identity mismatch")
	}
	name := sanitizeName(meta.NativeSessionID) + "-" + digest + ".jsonl"
	archive := filepath.Join(directory, name)
	if err := os.Link(staging.Name(), archive); err != nil {
		if !os.IsExist(err) {
			return nativecapture.Result{}, err
		}
		existing, err := os.Open(archive)
		if err != nil {
			return nativecapture.Result{}, err
		}
		verified := sha256.New()
		_, copyErr := io.Copy(verified, captureReader{ctx: ctx, source: existing})
		closeErr := existing.Close()
		if copyErr != nil {
			return nativecapture.Result{}, copyErr
		}
		if closeErr != nil {
			return nativecapture.Result{}, closeErr
		}
		if hex.EncodeToString(verified.Sum(nil)) != digest {
			return nativecapture.Result{}, fmt.Errorf("immutable native archive digest mismatch")
		}
	}
	if err := syncArchiveDirectory(directory); err != nil {
		return nativecapture.Result{}, err
	}
	return nativecapture.Result{ArchivePath: archive}, nil
}

func syncArchiveDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type captureReader struct {
	ctx    context.Context
	source io.Reader
}

func (r captureReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}
