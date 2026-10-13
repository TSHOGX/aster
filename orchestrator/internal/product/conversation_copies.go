package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/shareblob"
)

type CopyRequest struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Session      string   `json:"session,omitempty"`
	Token        string   `json:"token,omitempty"`
	ProjectID    string   `json:"project_id"`
	ProjectTitle string   `json:"project_title,omitempty"`
	Title        string   `json:"title"`
	Paths        []string `json:"paths"`
}
type ConversationCopy struct {
	Title                                    string `json:"title"`
	retry                                    int64
	ID                                       string       `json:"id"`
	ProjectID                                string       `json:"project_id"`
	SessionID                                string       `json:"session_id"`
	State                                    string       `json:"state"`
	Committed                                bool         `json:"committed"`
	Error                                    string       `json:"error"`
	Files                                    []SharedFile `json:"files"`
	actor, kind, source, tokenDigest, bundle string
	revision                                 int64
	paths                                    []string
}

const copyColumns = `id,actor,source_kind,source_id,source_token_digest,source_revision,target_project,target_session,title,paths,files,bundle_id,state,committed,error,retry_generation`

func scanCopy(row scanner) (v ConversationCopy, err error) {
	var paths, files string
	err = missing(row.Scan(&v.ID, &v.actor, &v.kind, &v.source, &v.tokenDigest, &v.revision, &v.ProjectID, &v.SessionID, &v.Title, &paths, &files, &v.bundle, &v.State, &v.Committed, &v.Error, &v.retry))
	if err == nil {
		err = json.Unmarshal([]byte(paths), &v.paths)
	}
	if err == nil {
		err = json.Unmarshal([]byte(files), &v.Files)
	}
	return
}
func (s *Store) ConversationCopy(ctx context.Context, actor, id string) (ConversationCopy, error) {
	if err := activeAccount(ctx, s.reader, actor); err != nil {
		return ConversationCopy{}, err
	}
	return scanCopy(s.reader.QueryRowContext(ctx, `SELECT `+copyColumns+` FROM conversation_copies WHERE id=? AND actor=?`, id, actor))
}
func (s *Store) ConversationCopies(ctx context.Context, actor string) ([]ConversationCopy, error) {
	if err := activeAccount(ctx, s.reader, actor); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+copyColumns+` FROM conversation_copies WHERE actor=? AND state<>'canceled' ORDER BY updated_at DESC,id DESC LIMIT 50`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConversationCopy{}
	for rows.Next() {
		v, e := scanCopy(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) copySource(ctx context.Context, q queryer, v ConversationCopy) error {
	if v.kind == "received" {
		if _, err := receivedSession(ctx, q, Scope{Actor: v.actor}, v.source); err != nil {
			return err
		}
		var revision int64
		var allow bool
		var raw string
		if err := q.QueryRowContext(ctx, `SELECT revision,allow_download,paths FROM conversation_grants WHERE session_id=? AND reader_id=? AND revoked=0 AND suspended=0`, v.source, v.actor).Scan(&revision, &allow, &raw); err != nil {
			return missing(err)
		}
		if revision != v.revision {
			return ErrConflict
		}
		var selected []string
		if err := json.Unmarshal([]byte(raw), &selected); err != nil {
			return err
		}
		for _, p := range v.paths {
			if !allow || !slices.Contains(selected, p) {
				return ErrForbidden
			}
		}
		return nil
	}
	if v.kind != "publicSnapshot" {
		return ErrInvalid
	}
	var allow bool
	var revision int64
	err := missing(q.QueryRowContext(ctx, `SELECT sh.allow_download,sh.access_revision FROM shares sh JOIN sessions s ON s.id=sh.session_id JOIN projects p ON p.id=sh.project_id JOIN accounts a ON a.id=p.owner_id WHERE sh.id=? AND sh.token_digest=? AND sh.state='published' AND sh.suspended=0 AND sh.expires_at>? AND s.deletion_intent=0 AND p.state NOT IN ('deleted','deleting') AND a.status='active'`, v.source, v.tokenDigest, s.timestamp()).Scan(&allow, &revision))
	if err != nil {
		return err
	}
	if revision != v.revision {
		return ErrConflict
	}
	for _, p := range v.paths {
		if !allow {
			return ErrForbidden
		}
		var exists bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM share_files WHERE share_id=? AND path=?)`, v.source, p).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}
func (s *Store) CreateConversationCopy(ctx context.Context, actor string, input CopyRequest) (ConversationCopy, error) {
	if !validID(input.ID) || !validID(input.ProjectID) || !validText(input.Title, 256) || len(input.Paths) > 100 {
		return ConversationCopy{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range input.Paths {
		if projectfiles.Path(p, false) != nil || seen[p] {
			return ConversationCopy{}, ErrInvalid
		}
		seen[p] = true
	}
	if input.Paths == nil {
		input.Paths = []string{}
	}
	raw, _ := json.Marshal(input)
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if old, err := s.ConversationCopy(ctx, actor, input.ID); err == nil {
		var saved string
		if e := s.reader.QueryRowContext(ctx, `SELECT request_digest FROM conversation_copies WHERE id=?`, input.ID).Scan(&saved); e != nil {
			return old, e
		}
		if digest != saved {
			return old, ErrConflict
		}
		return old, nil
	} else if !errors.Is(err, ErrNotFound) {
		return old, err
	}
	v := ConversationCopy{ID: input.ID, actor: actor, kind: input.Kind, source: input.Session, ProjectID: input.ProjectID, SessionID: copyIdentity(input.ID, "session"), Title: input.Title, paths: input.Paths, bundle: copyIdentity(input.ID, "bundle"), Files: []SharedFile{}, State: "preparing"}
	var sourceBundle string
	var sourcePaths []string
	if input.Kind == "received" {
		meta, err := s.ReceivedConversation(ctx, actor, input.Session)
		if err != nil {
			return v, err
		}
		if !meta.CanCopy {
			return v, Admission("no_complete_turn", ErrPending)
		}
		v.revision = meta.Revision
		sourcePaths = meta.Paths
	} else if input.Kind == "publicSnapshot" {
		meta, err := s.PublicBundle(ctx, input.Token)
		if err == nil && meta.Frontier == 0 {
			return v, Admission("no_complete_turn", ErrPending)
		}
		if err != nil {
			return v, err
		}
		v.source, v.tokenDigest, v.revision, sourceBundle = meta.ID, tokenDigest(input.Token), meta.AccessRevision, meta.BundleID
		for _, f := range meta.Files {
			sourcePaths = append(sourcePaths, f.Path)
		}
	} else {
		return v, ErrInvalid
	}
	if err := s.copySource(ctx, s.reader, v); err != nil {
		return v, err
	}
	if input.ProjectTitle != "" {
		if _, err := s.CreateProject(ctx, actor, input.ProjectID, input.ProjectTitle); err != nil {
			return v, err
		}
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		var existingDigest, existingActor string
		e := tx.QueryRowContext(ctx, `SELECT request_digest,actor FROM conversation_copies WHERE id=?`, v.ID).Scan(&existingDigest, &existingActor)
		if e == nil {
			if existingActor != actor {
				return ErrNotFound
			}
			if existingDigest != digest {
				return ErrConflict
			}
			var readErr error
			v, readErr = scanCopy(tx.QueryRowContext(ctx, `SELECT `+copyColumns+` FROM conversation_copies WHERE id=?`, v.ID))
			return readErr
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}

		if state, err := releaseState(ctx, tx); err != nil {
			return err
		} else if state.Phase != "open" {
			return ErrDraining
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_copies WHERE actor=? AND state IN ('preparing','committed')`, actor).Scan(&pending); err != nil {
			return err
		}
		if pending >= 8 {
			return ErrCapacity
		}
		scope := Scope{Actor: actor, ProjectID: input.ProjectID}
		if _, err := projectAction(ctx, tx, scope, ActionAgentsRun); err != nil {
			return err
		}
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if err := s.copySource(ctx, tx, v); err != nil {
			return err
		}
		if len(v.paths) > 0 {
			if err := authorizeAction(ctx, tx, actor, ActionFilesUpload); err != nil {
				return err
			}
		}
		if sourceBundle == "" {
			frontier, err := freezeConversation(ctx, tx, v.bundle, v.source, v.Title, s.timestamp())
			if err != nil {
				return err
			}
			if frontier == 0 {
				return Admission("no_complete_turn", ErrPending)
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_bundles SELECT ?,?,frontier,? FROM conversation_bundles WHERE id=? AND frontier>0`, v.bundle, v.Title, s.timestamp(), sourceBundle); err != nil {
				return err
			}
			for _, table := range []string{"bundle_items", "bundle_messages", "bundle_turns"} {
				columns := map[string]string{"bundle_items": "id,turn_id,position,block_index,data,detail", "bundle_messages": "id,turn_id,role,content,sequence,created_at", "bundle_turns": "id,sequence,content,data"}[table]
				if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` SELECT ?,`+columns+` FROM `+table+` WHERE bundle_id=?`, v.bundle, sourceBundle); err != nil {
					return err
				}
			}
		}
		paths, _ := json.Marshal(v.paths)
		sourcePathsRaw, _ := json.Marshal(sourcePaths)
		_, err := tx.ExecContext(ctx, `INSERT INTO conversation_copies(id,actor,request_digest,source_kind,source_id,source_token_digest,source_revision,target_project,target_session,title,paths,source_paths,bundle_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'preparing',?,?)`, v.ID, actor, digest, v.kind, v.source, v.tokenDigest, v.revision, v.ProjectID, v.SessionID, v.Title, string(paths), string(sourcePathsRaw), v.bundle, s.timestamp(), s.timestamp())
		return err
	})
	return v, err
}
func copyIdentity(id, part string) string {
	sum := sha256.Sum256([]byte(id + "/" + part))
	return fmt.Sprintf("copy-%x", sum[:16])
}

func (s *Store) CancelConversationCopy(ctx context.Context, actor, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := activeAccount(ctx, tx, actor); err != nil {
			return err
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE conversation_copies SET state='canceled',error='',updated_at=? WHERE id=? AND actor=? AND committed=0`, s.timestamp(), id, actor)); err != nil {
			return err
		}
		for _, table := range []string{"bundle_items", "bundle_messages", "bundle_turns"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE bundle_id=(SELECT bundle_id FROM conversation_copies WHERE id=?)`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) RetryConversationCopy(ctx context.Context, actor, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := activeAccount(ctx, tx, actor); err != nil {
			return err
		}
		v, err := scanCopy(tx.QueryRowContext(ctx, `SELECT `+copyColumns+` FROM conversation_copies WHERE id=? AND actor=?`, id, actor))
		if err != nil {
			return err
		}
		if v.State != "failed" {
			return ErrConflict
		}
		if state, err := releaseState(ctx, tx); err != nil {
			return err
		} else if state.Phase != "open" {
			return ErrDraining
		}
		scope := Scope{Actor: actor, ProjectID: v.ProjectID}
		if _, err := projectAction(ctx, tx, scope, ActionAgentsRun); err != nil {
			return err
		}
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if !v.Committed {
			if err = s.copySource(ctx, tx, v); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE conversation_copies SET state=CASE WHEN committed=1 THEN 'committed' ELSE 'preparing' END,error='',cleaned=0,retry_generation=retry_generation+1,updated_at=? WHERE id=?`, s.timestamp(), id)
		return err
	})
}
func (sh *Shares) CopyStep(ctx context.Context) error {
	started := time.Now()
	if !sh.copyMu.TryLock() {
		return nil
	}
	defer sh.copyMu.Unlock()
	release, err := sh.Store.beginRuntimeWork(ctx, runtimeMaintenance)
	if err != nil {
		return err
	}
	defer release()
	if err := sh.cleanupCopy(ctx); err != nil {
		return err
	}
	v, err := scanCopy(sh.Store.reader.QueryRowContext(ctx, `SELECT `+copyColumns+` FROM conversation_copies WHERE state IN ('preparing','committed') ORDER BY updated_at,id LIMIT 1`))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = sh.Store.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE conversation_copies SET attempts=attempts+1,updated_at=? WHERE id=?`, sh.Store.timestamp(), v.ID)
		return e
	}); err != nil {
		return err
	}
	err = sh.advanceCopy(ctx, v)
	outcome := "complete"
	if err != nil {
		outcome = "pending"
		if !errors.Is(err, ErrPending) {
			outcome = "failed"
		}
	}
	if outcome != "pending" {
		slog.Info("conversation copy step", "copy_id", v.ID, "stage", v.State, "outcome", outcome, "duration_ms", time.Since(started).Milliseconds())
	}
	if err != nil && !errors.Is(err, ErrPending) && !errors.Is(err, ErrDraining) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// Store a bounded reason, never a source token or message body.
		detail := "Copy could not finish. Check source access, target permissions and file availability."
		_ = sh.Store.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE conversation_copies SET state='failed',error=?,updated_at=? WHERE id=? AND state IN ('preparing','committed')`, detail, sh.Store.timestamp(), v.ID)
			return e
		})
	}
	if err != nil {
		var committed bool
		if e := sh.Store.reader.QueryRowContext(ctx, `SELECT committed FROM conversation_copies WHERE id=?`, v.ID).Scan(&committed); e == nil && !committed {
			if directory, e := sh.directory(copyIdentity(v.ID, "files")); e == nil {
				_ = os.RemoveAll(directory)
			}
		}
	}
	return err
}
func (sh *Shares) advanceCopy(ctx context.Context, v ConversationCopy) error {
	directory, err := sh.directory(copyIdentity(v.ID, "files"))
	if err != nil {
		return err
	}
	blobs, err := shareblob.New(directory)
	if err != nil {
		return err
	}
	scope := Scope{Actor: v.actor, ProjectID: v.ProjectID}
	if !v.Committed {
		if err = sh.Store.copySource(ctx, sh.Store.reader, v); err != nil {
			return err
		}
		files := []SharedFile{}
		var total int64
		for _, name := range v.paths {
			var reader io.ReadCloser
			if v.kind == "received" {
				if sh.Files == nil {
					return ErrPending
				}
				reader, err = sh.Files.CaptureReceived(ctx, v.actor, v.source, name, true)
			} else {
				var digest string
				err = sh.Store.reader.QueryRowContext(ctx, `SELECT digest FROM share_files WHERE share_id=? AND path=?`, v.source, name).Scan(&digest)
				if err == nil {
					sourceDir, e := sh.directory(v.source)
					err = e
					if err == nil {
						source, e := shareblob.New(sourceDir)
						err = e
						if err == nil {
							reader, _, err = source.Open(digest)
						}
					}
				}
			}
			if err != nil {
				return err
			}
			digest, size, e := blobs.Put(reader, projectfiles.MaxTransfer)
			closeErr := reader.Close()
			if e = errors.Join(e, closeErr); e != nil {
				return e
			}
			total += size
			if total > 256<<20 {
				return ErrCapacity
			}
			files = append(files, SharedFile{Path: name, Digest: digest, Size: size})
		}
		if len(files) > 0 {
			if err = sh.prepareCopyStorage(ctx, scope, total); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(files)
		err = sh.Store.write(ctx, func(tx *sql.Tx) error {
			if err := sh.Store.copySource(ctx, tx, v); err != nil {
				return err
			}
			if _, err := projectAction(ctx, tx, scope, ActionAgentsRun); err != nil {
				return err
			}
			if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
				return err
			}
			if len(files) > 0 {
				if err := authorizeAction(ctx, tx, v.actor, ActionFilesUpload); err != nil {
					return err
				}
				if err := admitTransfer(ctx, tx, v.actor, total); err != nil {
					return err
				}
			}
			return changed(tx.ExecContext(ctx, `UPDATE conversation_copies SET state='committed',committed=1,files=?,updated_at=? WHERE id=? AND state='preparing'`, string(raw), sh.Store.timestamp(), v.ID))
		})
		if err != nil {
			return err
		}
		v.Files = files
		v.Committed = true
	}
	for _, file := range v.Files {
		if sh.Files == nil {
			return ErrPending
		}
		target := path.Join("Shared imports", v.ID, file.Path)
		if err = sh.ensureCopyDirectories(ctx, scope, v, path.Dir(target)); err != nil {
			return err
		}
		input, _, e := blobs.Open(file.Digest)
		if e != nil {
			return e
		}
		operationID, e := sh.copyFileOperationID(ctx, v, "write/"+file.Path)
		if e != nil {
			input.Close()
			return e
		}
		if _, readErr := fileOperation(ctx, sh.Store.reader, operationID); errors.Is(readErr, ErrNotFound) {
			page, readErr := sh.Files.List(ctx, scope, projectfiles.DirectoryQuery{Path: path.Dir(target), Name: path.Base(target), Seek: "around", Limit: 100})
			if readErr != nil {
				input.Close()
				return readErr
			}
			for _, entry := range page.Entries {
				if entry.Name == path.Base(target) {
					input.Close()
					return Admission("copy_target_exists", ErrConflict)
				}
			}
		} else if readErr != nil {
			input.Close()
			return readErr
		}
		op, e := sh.Files.Upload(ctx, scope, operationID, projectfiles.Request{Operation: "write", Path: target, Size: file.Size, Digest: file.Digest}, input)
		closeErr := input.Close()
		if e = errors.Join(e, closeErr); e != nil {
			return e
		}
		if !op.Settled {
			return ErrPending
		}
	}
	err = sh.Store.write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM conversation_copies WHERE id=?`, v.ID).Scan(&state); err != nil {
			return err
		}
		if state == "complete" {
			return nil
		}
		if state != "committed" {
			return ErrConflict
		}
		if _, err := sh.Store.createSession(ctx, tx, scope, v.SessionID, v.Title); err != nil {
			return err
		}
		mapping := map[string]string{}
		var originalRaw string
		if err := tx.QueryRowContext(ctx, `SELECT source_paths FROM conversation_copies WHERE id=?`, v.ID).Scan(&originalRaw); err != nil {
			return err
		}
		var originals []string
		if err := json.Unmarshal([]byte(originalRaw), &originals); err != nil {
			return err
		}
		for _, p := range originals {
			mapping[p] = ""
		}
		for _, file := range v.Files {
			mapping[file.Path] = path.Join("Shared imports", v.ID, file.Path)
		}
		raw, _ := json.Marshal(mapping)
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_imports(session_id,bundle_id,file_map) VALUES(?,?,?)`, v.SessionID, v.bundle, string(raw)); err != nil {
			return err
		}
		if err := installBundle(ctx, tx, v.SessionID, v.ProjectID, v.bundle, sh.Store.timestamp()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE conversation_copies SET state='complete',error='',updated_at=? WHERE id=?`, sh.Store.timestamp(), v.ID)
		return err
	})
	if err != nil {
		return err
	}
	// Publication succeeded. Cleanup is independent maintenance: a filesystem
	// failure must never turn a usable copy into a failed/replayed import.
	return nil
}
func (sh *Shares) ensureCopyDirectories(ctx context.Context, scope Scope, copy ConversationCopy, directory string) error {
	current := ""
	for _, part := range strings.Split(directory, "/") {
		parent := current
		current = path.Join(current, part)
		page, err := sh.Files.List(ctx, scope, projectfiles.DirectoryQuery{Path: parent, Name: part, Seek: "around", Limit: 100})
		if err != nil {
			return err
		}
		exists := false
		for _, entry := range page.Entries {
			if entry.Name == part && entry.Kind == "directory" {
				exists = true
			}
		}
		if exists {
			continue
		}
		operationID, err := sh.copyFileOperationID(ctx, copy, "mkdir/"+current)
		if err != nil {
			return err
		}
		op, err := sh.Files.Manage(ctx, scope, operationID, projectfiles.Request{Operation: "mkdir", Path: current})
		if err != nil {
			return err
		}
		if !op.Settled {
			return ErrPending
		}
	}
	return nil
}

func (sh *Shares) prepareCopyStorage(ctx context.Context, scope Scope, bytes int64) error {
	for _, action := range []ProductAction{ActionFilesUpload, ActionFilesManage, ActionFilesList} {
		if _, err := projectAction(ctx, sh.Store.reader, scope, action); err != nil {
			return err
		}
	}
	p, err := ownedProject(ctx, sh.Store.reader, scope)
	if err != nil {
		return err
	}
	if p.State == "active" {
		return admitTransfer(ctx, sh.Store.reader, scope.Actor, bytes)
	}
	_, runctx, _, release, paused, err := sh.Files.workspaceReader().pausedVolume(ctx, scope)
	if err != nil {
		return err
	}
	if !paused {
		return ErrPending
	}
	defer release()
	_, err = sh.Files.ensureWorkspaceStorage(runctx, scope, ActionFilesUpload, bytes)
	return err
}

func (sh *Shares) copyFileOperationID(ctx context.Context, v ConversationCopy, part string) (string, error) {
	var id string
	for generation := int64(0); generation <= v.retry; generation++ {
		suffix := part
		if generation > 0 {
			suffix = fmt.Sprintf("%s/retry/%d", part, generation)
		}
		id = copyIdentity(v.ID, suffix)
		operation, err := fileOperation(ctx, sh.Store.reader, id)
		if errors.Is(err, ErrNotFound) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		if operation.State != "failed" || !operation.Settled {
			return id, nil
		}
	}
	return id, nil
}

func (sh *Shares) cleanupCopy(ctx context.Context) error {
	var id string
	err := sh.Store.reader.QueryRowContext(ctx, `SELECT id FROM conversation_copies WHERE cleaned=0 AND (state IN ('complete','canceled') OR state='failed' AND committed=0) ORDER BY updated_at,id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	directory, err := sh.directory(copyIdentity(id, "files"))
	if err != nil {
		return err
	}
	if err = os.RemoveAll(directory); err != nil {
		return err
	}
	return sh.Store.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE conversation_copies SET cleaned=1 WHERE id=? AND (state IN ('complete','canceled') OR state='failed' AND committed=0)`, id)
		return e
	})
}
