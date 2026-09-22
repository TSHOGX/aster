package product

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/sqlitex"
)

type Share struct {
	ID        string       `json:"id"`
	ProjectID string       `json:"project_id"`
	SessionID string       `json:"session_id"`
	Title     string       `json:"title"`
	Frontier  int64        `json:"frontier"`
	State     string       `json:"state"`
	ExpiresAt string       `json:"expires_at"`
	CreatedAt string       `json:"created_at"`
	Files     []SharedFile `json:"files"`
}
type SharedFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

const shareColumns = `id,project_id,session_id,title,frontier,state,expires_at,created_at`

func scanShare(row scanner) (s Share, err error) {
	err = missing(row.Scan(&s.ID, &s.ProjectID, &s.SessionID, &s.Title, &s.Frontier, &s.State, &s.ExpiresAt, &s.CreatedAt))
	s.Files = []SharedFile{}
	return
}
func tokenDigest(token string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(token))) }
func newShareToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

type Shares struct {
	Store     *Store
	Files     *Files
	Directory string
	mu        sync.Mutex
	active    map[string]bool
}

func (sh *Shares) directory(id string) (string, error) {
	if !filepath.IsAbs(sh.Directory) || !validID(id) {
		return "", ErrInvalid
	}
	return filepath.Join(sh.Directory, id), nil
}

// Create publishes only after every selected file has been frozen. Public links
// expose an immutable conversation frontier and these exact host-owned bytes.
func (sh *Shares) Create(ctx context.Context, scope Scope, session string, paths []string, expires time.Time) (Share, string, error) {
	var out Share
	if len(paths) > 16 || expires.Before(sh.Store.now().Add(time.Minute)) || expires.After(sh.Store.now().Add(90*24*time.Hour)) {
		return out, "", ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if projectfiles.Path(p, false) != nil || seen[p] {
			return out, "", ErrInvalid
		}
		seen[p] = true
	}
	id := uuid.NewString()
	directory, err := sh.directory(id)
	if err != nil {
		return out, "", err
	}
	token, err := newShareToken()
	if err != nil {
		return out, "", err
	}
	sh.mu.Lock()
	if len(sh.active) >= 8 {
		sh.mu.Unlock()
		return out, "", ErrCapacity
	}
	if sh.active == nil {
		sh.active = map[string]bool{}
	}
	sh.active[id] = true
	sh.mu.Unlock()
	defer func() { sh.mu.Lock(); delete(sh.active, id); sh.mu.Unlock() }()
	err = sh.Store.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		var frontier int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),0) FROM messages WHERE session_id=?`, session).Scan(&frontier); err != nil {
			return err
		}
		raw, _ := json.Marshal(paths)
		stamp := sqlitex.FormatTime(expires)
		if _, err = tx.ExecContext(ctx, `INSERT INTO shares(id,project_id,session_id,token_digest,title,frontier,paths,state,expires_at,created_at) VALUES(?,?,?,?,?,?,?,'pending',?,?)`, id, scope.ProjectID, session, tokenDigest(token), child.Title, frontier, string(raw), stamp, sh.Store.timestamp()); err != nil {
			return err
		}
		out = Share{ID: id, ProjectID: scope.ProjectID, SessionID: session, Title: child.Title, Frontier: frontier, State: "pending", ExpiresAt: stamp, CreatedAt: sh.Store.timestamp(), Files: []SharedFile{}}
		return nil
	})
	if err != nil {
		return out, "", err
	}
	fail := func(err error) (Share, string, error) {
		settle, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_ = sh.Store.write(settle, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(settle, `UPDATE shares SET state='failed' WHERE id=? AND state='pending'`, id)
			return e
		})
		out.State = "failed"
		return out, "", err
	}
	blobs, err := shareblob.New(directory)
	if err != nil {
		return fail(err)
	}
	var total int64
	for _, name := range paths {
		if sh.Files == nil {
			return fail(ErrInvalid)
		}
		capture, captureErr := sh.Files.Capture(ctx, scope, name)
		if captureErr != nil {
			return fail(captureErr)
		}
		digest, n, putErr := blobs.Put(capture, 16<<20)
		closeErr := capture.Close()
		if err = errors.Join(putErr, closeErr); err != nil {
			return fail(err)
		}
		total += n
		if total > 64<<20 {
			return fail(ErrCapacity)
		}
		out.Files = append(out.Files, SharedFile{Path: name, Digest: digest, Size: n})
	}
	err = sh.Store.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		if err = changed(tx.ExecContext(ctx, `UPDATE shares SET state='published' WHERE id=? AND state='pending' AND expires_at>?`, id, sh.Store.timestamp())); err != nil {
			return err
		}
		for _, file := range out.Files {
			if _, err = tx.ExecContext(ctx, `INSERT INTO share_files(share_id,path,digest,size) VALUES(?,?,?,?)`, id, file.Path, file.Digest, file.Size); err != nil {
				return err
			}
		}
		return sh.Store.bumpSession(ctx, tx, scope.ProjectID, session, "share.published")
	})
	if err != nil {
		return fail(err)
	}
	out.State = "published"
	return out, token, nil
}
func (s *Store) Shares(ctx context.Context, scope Scope, session, after string, limit int) ([]Share, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+shareColumns+` FROM shares WHERE session_id=? AND id>? ORDER BY id LIMIT ?`, session, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		share, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, share)
	}
	return out, rows.Err()
}
func (s *Store) RevokeShare(ctx context.Context, scope Scope, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := authorizedProject(ctx, tx, scope); err != nil {
			return err
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE shares SET state='revoked' WHERE id=? AND project_id=?`, id, scope.ProjectID)); err != nil {
			return err
		}
		return s.bumpProject(ctx, tx, scope.ProjectID, "share.revoked")
	})
}
func (s *Store) RotateShare(ctx context.Context, scope Scope, id string) (string, error) {
	token, err := newShareToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		return changed(tx.ExecContext(ctx, `UPDATE shares SET token_digest=? WHERE id=? AND project_id=? AND state='published' AND expires_at>?`, tokenDigest(token), id, scope.ProjectID, s.timestamp()))
	})
	return token, err
}
func (s *Store) PublicShare(ctx context.Context, token string) (Share, error) {
	if len(token) != 43 {
		return Share{}, ErrNotFound
	}
	// Parent state is checked even though revocation triggers also enforce it.
	share, err := scanShare(s.reader.QueryRowContext(ctx, `SELECT `+shareColumns+` FROM shares WHERE token_digest=? AND state='published' AND expires_at>? AND EXISTS(SELECT 1 FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.id=shares.session_id AND s.deletion_intent=0 AND p.state NOT IN ('deleting','deleted'))`, tokenDigest(token), s.timestamp()))
	if err != nil {
		return share, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT path,digest,size FROM share_files WHERE share_id=? ORDER BY path LIMIT 16`, share.ID)
	if err != nil {
		return Share{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var f SharedFile
		if err = rows.Scan(&f.Path, &f.Digest, &f.Size); err != nil {
			return Share{}, err
		}
		share.Files = append(share.Files, f)
	}
	return share, rows.Err()
}
func (s *Store) PublicMessages(ctx context.Context, token string, after int64, limit int) ([]Message, error) {
	share, err := s.PublicShare(ctx, token)
	if err != nil {
		return nil, err
	}
	messages, err := s.messages(ctx, share.ProjectID, share.SessionID, after, limit)
	if err != nil {
		return nil, err
	}
	out := []Message{}
	for _, m := range messages {
		if m.Sequence > share.Frontier {
			break
		}
		m.Native = nil
		m.ProjectID = ""
		m.SessionID = ""
		m.TurnID = ""
		out = append(out, m)
	}
	return out, nil
}
func (sh *Shares) OpenFile(ctx context.Context, token, path string) (*os.File, SharedFile, error) {
	share, err := sh.Store.PublicShare(ctx, token)
	if err != nil {
		return nil, SharedFile{}, err
	}
	for _, file := range share.Files {
		if file.Path == path {
			directory, err := sh.directory(share.ID)
			if err != nil {
				return nil, file, err
			}
			blobs, err := shareblob.New(directory)
			if err != nil {
				return nil, file, err
			}
			f, info, err := blobs.Open(file.Digest)
			if err != nil {
				if errors.Is(err, shareblob.ErrNotFound) {
					err = ErrNotFound
				}
				return nil, file, err
			}
			if info.Size() != file.Size {
				f.Close()
				return nil, file, ErrConflict
			}
			return f, file, nil
		}
	}
	return nil, SharedFile{}, ErrNotFound
}

// Reconcile reclaims only per-share host storage. Pending publications from a
// previous owner are failed, never published from partial file selections.
func (sh *Shares) Reconcile(ctx context.Context, limit int) error {
	rows, err := sh.Store.reader.QueryContext(ctx, `SELECT id FROM shares WHERE cleaned=0 AND (state IN ('pending','failed','revoked') OR expires_at<=?) ORDER BY created_at,id LIMIT ?`, sh.Store.timestamp(), pageSize(limit))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		sh.mu.Lock()
		active := sh.active[id]
		sh.mu.Unlock()
		if active {
			continue
		}
		// Close publication/read authority before unlinking, including on expiry.
		err = sh.Store.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE shares SET state=CASE WHEN state='pending' THEN 'failed' ELSE 'revoked' END WHERE id=? AND cleaned=0`, id)
			return err
		})
		if err != nil {
			return err
		}
		directory, err := sh.directory(id)
		if err != nil {
			return err
		}
		if err = os.RemoveAll(directory); err != nil {
			return err
		}
		err = sh.Store.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM share_files WHERE share_id=?`, id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE shares SET cleaned=1 WHERE id=?`, id)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
