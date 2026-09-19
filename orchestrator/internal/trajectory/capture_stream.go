package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
	"harness-platform/orchestrator/internal/nativecapture"
)

type CaptureIdentity struct{ SessionID, UserID, Status, NativeSessionID string }

// CaptureStream captures bytes once, then parses exactly that durable snapshot.
// Engine-backed producers can supply a confined source stream; downstream
// memory/evolve consumers still receive the existing raw-source/event records.
func (c *Capturer) CaptureStream(ctx context.Context, identity CaptureIdentity, source io.Reader, modified time.Time) (CaptureResult, error) {
	return c.captureStream(ctx, identity, source, modified, "", 0)
}

func (c *Capturer) CaptureNativeSnapshot(ctx context.Context, p nativecapture.Request, source io.Reader) (nativecapture.Result, error) {
	if c == nil || c.store == nil || p.Size <= 0 || p.Size > 512<<20 || len(p.NativeDigest) != 64 {
		return nativecapture.Result{}, fmt.Errorf("enabled capturer and bounded native snapshot evidence required")
	}
	result, err := c.captureStream(ctx, CaptureIdentity{SessionID: p.SessionID, UserID: p.UserID, Status: p.Status, NativeSessionID: p.NativeID}, io.LimitReader(source, p.Size+1), p.CreatedAt, p.NativeDigest, p.Size)
	return nativecapture.Result{ArchivePath: result.ArchivePath, Skipped: result.Skipped, Reason: result.Reason}, err
}

func (c *Capturer) captureStream(ctx context.Context, identity CaptureIdentity, source io.Reader, modified time.Time, expectedDigest string, expectedSize int64) (CaptureResult, error) {
	if c == nil {
		return CaptureResult{SessionID: identity.SessionID, Skipped: true, Reason: "capturer disabled"}, nil
	}
	if identity.SessionID == "" || sanitizeName(identity.SessionID) != identity.SessionID {
		return CaptureResult{}, fmt.Errorf("safe capture session identity required")
	}
	directory := filepath.Join(c.archiveRoot, identity.SessionID)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return CaptureResult{}, err
	}
	if err := syncArchiveDirectory(c.archiveRoot); err != nil {
		return CaptureResult{}, err
	}
	staging, err := os.CreateTemp(directory, ".capture-*")
	if err != nil {
		return CaptureResult{}, err
	}
	defer staging.Close()
	defer os.Remove(staging.Name())
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(staging, hash), captureReader{ctx: ctx, source: source})
	if err != nil {
		return CaptureResult{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expectedDigest != "" && (digest != expectedDigest || size != expectedSize) {
		return CaptureResult{}, fmt.Errorf("captured native snapshot does not match committed size/digest")
	}
	if err := staging.Chmod(0444); err != nil {
		return CaptureResult{}, err
	}
	if err := staging.Sync(); err != nil {
		return CaptureResult{}, err
	}
	if _, err := staging.Seek(0, io.SeekStart); err != nil {
		return CaptureResult{}, err
	}
	events, meta, err := ParsePiSession(staging)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("parse captured native source: %w", err)
	}
	if meta.IsTest {
		return CaptureResult{SessionID: identity.SessionID, Skipped: true, Reason: "test session"}, nil
	}
	if identity.NativeSessionID != "" && identity.NativeSessionID != meta.NativeSessionID {
		return CaptureResult{}, fmt.Errorf("captured native session identity mismatch")
	}
	name := sanitizeName(meta.NativeSessionID) + "-" + digest + ".jsonl"
	archive := filepath.Join(directory, name)
	if err := os.Link(staging.Name(), archive); err != nil {
		if !os.IsExist(err) {
			return CaptureResult{}, err
		}
		existing, err := os.Open(archive)
		if err != nil {
			return CaptureResult{}, err
		}
		verified := sha256.New()
		_, copyErr := io.Copy(verified, captureReader{ctx: ctx, source: existing})
		closeErr := existing.Close()
		if copyErr != nil {
			return CaptureResult{}, copyErr
		}
		if closeErr != nil {
			return CaptureResult{}, closeErr
		}
		if hex.EncodeToString(verified.Sum(nil)) != digest {
			return CaptureResult{}, fmt.Errorf("immutable native archive digest mismatch")
		}
	}
	if err := syncArchiveDirectory(directory); err != nil {
		return CaptureResult{}, err
	}
	if err := c.store.UpsertRawSource(ctx, identity.SessionID, archive, "pi_jsonl", meta.NativeSessionID, size, modified); err != nil {
		return CaptureResult{}, err
	}
	snapshot := memorystore.TrajectorySnapshot{SessionID: identity.SessionID, Status: identity.Status, Agent: "pi", UserID: identity.UserID, StartedAt: meta.StartedAt, EndedAt: meta.EndedAt, TurnCount: meta.TurnCount, MessageCount: len(events), Fingerprint: meta.Fingerprint, Fidelity: "full"}
	for i := range events {
		events[i].SessionID = identity.SessionID
		events[i].Idx = i
	}
	if err := c.store.ReplaceTrajectoryEvents(ctx, snapshot, events); err != nil {
		return CaptureResult{}, err
	}
	return CaptureResult{SessionID: identity.SessionID, Fidelity: "full", EventCount: len(events), ArchivePath: archive}, nil
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
