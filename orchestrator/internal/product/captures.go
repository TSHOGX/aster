package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"harness-platform/orchestrator/internal/sandbox"
)

type NativeCapture struct {
	TurnID, ProjectID, SessionID, IncarnationID, Path, Digest, State, HostPath string
	Size                                                                       int64
	Reference                                                                  sandbox.Reference
}
type CaptureReader interface {
	ReadNativeCapture(context.Context, sandbox.Reference, string) (io.ReadCloser, error)
}

// Captures exports immutable completed native bytes. Once HostPath is committed,
// archive retry is host-only work and doesn't retain Project activity.
type Captures struct {
	mu        sync.RWMutex
	Store     *Store
	Backend   CaptureReader
	Directory string
}

func (s *Store) PendingCaptures(ctx context.Context, limit int) ([]NativeCapture, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT c.turn_id,i.project_id,i.session_id,c.incarnation_id,c.path,c.digest,c.state,c.host_path,c.size,r.store_id,r.sandbox_id,r.epoch,r.mutation_revision FROM native_captures c JOIN agent_incarnations i ON i.id=c.incarnation_id JOIN project_resources r ON r.project_id=i.project_id JOIN projects p ON p.id=i.project_id WHERE c.state='guest' AND p.state NOT IN ('deleting','deleted') ORDER BY row_number() OVER (PARTITION BY i.session_id ORDER BY c.attempted_at,c.rowid),c.attempted_at,c.rowid LIMIT ?`, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NativeCapture
	for rows.Next() {
		var c NativeCapture
		if err := rows.Scan(&c.TurnID, &c.ProjectID, &c.SessionID, &c.IncarnationID, &c.Path, &c.Digest, &c.State, &c.HostPath, &c.Size, &c.Reference.StoreID, &c.Reference.SandboxID, &c.Reference.Epoch, &c.Reference.MutationRevision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (c *Captures) Export(ctx context.Context, capture NativeCapture) error {
	releaseMaintenance, admissionErr := c.Store.beginRuntimeWork(ctx, runtimeMaintenance)
	if admissionErr != nil {
		return admissionErr
	}
	defer releaseMaintenance()

	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, err := c.Store.exec(ctx, `UPDATE native_captures SET attempted_at=? WHERE turn_id=? AND path=?`, c.Store.timestamp(), capture.TurnID, capture.Path); err != nil {
		return err
	}
	var deleting bool
	if err := c.Store.reader.QueryRowContext(ctx, `SELECT p.state IN ('deleting','deleted') OR s.deletion_intent>0 FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.id=? AND p.id=?`, capture.SessionID, capture.ProjectID).Scan(&deleting); err != nil {
		return missing(err)
	}
	if deleting {
		return ErrDeleting
	}

	if !filepath.IsAbs(c.Directory) || capture.Size < 0 || capture.Size > 64<<20 || !strings.HasPrefix(capture.Digest, "sha256:") {
		return ErrInvalid
	}
	// Selection comes from the committed capture ledger, never a browser path.
	var path, digest, state string
	var size int64
	if err := c.Store.reader.QueryRowContext(ctx, `SELECT path,digest,size,state FROM native_captures WHERE turn_id=? AND path=? AND incarnation_id=?`, capture.TurnID, capture.Path, capture.IncarnationID).Scan(&path, &digest, &size, &state); err != nil {
		return err
	}
	if path != capture.Path || digest != capture.Digest || size != capture.Size {
		return ErrConflict
	}
	if state != "guest" {
		return nil
	}
	source, err := c.Backend.ReadNativeCapture(ctx, capture.Reference, path)
	if err != nil {
		return err
	}
	defer source.Close()
	directory := filepath.Join(c.Directory, capture.ProjectID, capture.SessionID)
	if !validID(capture.ProjectID) || !validID(capture.SessionID) {
		return ErrInvalid
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, "capture-*.tmp")
	if err != nil {
		return err
	}
	defer temp.Close()
	defer os.Remove(temp.Name())
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(source, capture.Size+1))
	if err != nil {
		return err
	}
	sum := fmt.Sprintf("sha256:%x", hash.Sum(nil))
	if n != capture.Size || sum != capture.Digest {
		return errors.New("native capture differs from committed digest")
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	destination := filepath.Join(directory, strings.TrimPrefix(sum, "sha256:")+".jsonl")
	if err = os.Rename(temp.Name(), destination); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	return c.Store.write(ctx, func(tx *sql.Tx) error {
		if err := changed(tx.ExecContext(ctx, `UPDATE native_captures SET host_path=?,state='exported' WHERE turn_id=? AND path=? AND digest=? AND state='guest'`, destination, capture.TurnID, capture.Path, capture.Digest)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_activity SET state='complete',updated_at=? WHERE id=? AND project_id=?`, c.Store.timestamp(), captureActivity(capture.TurnID, capture.Path), capture.ProjectID); err != nil {
			return err
		}
		return c.Store.activity(ctx, tx, capture.ProjectID)
	})
}

func captureActivity(turn, path string) string { return "capture/" + turn + "/" + filepath.Base(path) }
