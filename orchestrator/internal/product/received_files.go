package product

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/sandbox"
	"slices"
	"strings"
)

func (s *Store) ReceivedFileRevision(ctx context.Context, actor, session, name string) (string, error) {
	child, err := receivedSession(ctx, s.reader, Scope{Actor: actor}, session)
	if err != nil {
		return "", err
	}
	var id string
	err = s.reader.QueryRowContext(ctx, `SELECT coalesce((SELECT id||'/'||updated_at||'/'||state FROM file_operations WHERE project_id=? AND mount_key='' AND (json_extract(request,'$.path')=? OR json_extract(request,'$.destination')=? OR instr(?,json_extract(request,'$.path')||'/')=1) ORDER BY updated_at DESC,id DESC LIMIT 1),'')`, child.ProjectID, name, name, name).Scan(&id)
	return id, err
}

type receivedFileKey struct{}
type receivedFileGrant struct {
	Session, Actor, Project, Path string
	Revision                      int64
	Download                      bool
}

func (f *Files) WatchReceived(ctx context.Context, actor, session, path string) (sandbox.FileWatch, error) {
	ctx, scope, err := f.Store.receivedFileContext(ctx, actor, session, path, false)
	if err != nil {
		return nil, err
	}
	r, err := f.workspaceReader().activeReadResources(ctx, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	watch, err := f.Backend.WatchWorkspace(ctx, r.Reference, r.Profile.RolePath("workspace"), 0)
	if err != nil {
		return nil, err
	}
	return &receivedFileWatch{FileWatch: watch, ctx: ctx, store: f.Store, scope: scope, path: path}, nil
}

type receivedFileWatch struct {
	sandbox.FileWatch
	ctx   context.Context
	store *Store
	scope Scope
	path  string
}

func (w *receivedFileWatch) Next() (sandbox.FileInvalidation, error) {
	for {
		event, err := w.FileWatch.Next()
		if err != nil {
			return event, err
		}
		if _, err = fileReadProject(w.ctx, w.store.reader, w.scope); err != nil {
			return sandbox.FileInvalidation{}, err
		}
		affects := func(p string) bool { return p == "" || p == w.path || strings.HasPrefix(w.path, p+"/") }
		if event.Reset || affects(event.Path) || event.OldPath != "" && affects(event.OldPath) {
			return sandbox.FileInvalidation{Sequence: event.Sequence, Reset: true}, nil
		}
	}
}

// This capability is created only after checking an exact grant and file path.
// It is never accepted by Project mutations, directory listing or mount access.
func (s *Store) receivedFileContext(ctx context.Context, actor, session, path string, download bool) (context.Context, Scope, error) {
	v, err := s.ReceivedConversation(ctx, actor, session)
	if err != nil {
		return nil, Scope{}, err
	}
	if !slices.Contains(v.Paths, path) {
		return nil, Scope{}, ErrNotFound
	}
	if download && !v.AllowDownload {
		return nil, Scope{}, ErrForbidden
	}
	child, err := receivedSession(ctx, s.reader, Scope{Actor: actor}, session)
	if err != nil {
		return nil, Scope{}, err
	}
	grant := receivedFileGrant{Session: session, Actor: actor, Project: child.ProjectID, Path: path, Revision: v.Revision, Download: download}
	return context.WithValue(ctx, receivedFileKey{}, grant), Scope{Actor: actor, ProjectID: child.ProjectID}, nil
}
func fileReadProject(ctx context.Context, q queryer, scope Scope) (Project, error) {
	grant, shared := ctx.Value(receivedFileKey{}).(receivedFileGrant)
	if !shared {
		return ownedProject(ctx, q, scope)
	}
	if scope.Actor != grant.Actor || scope.ProjectID != grant.Project || fileMount(ctx) != "" {
		return Project{}, ErrNotFound
	}
	if _, err := receivedSession(ctx, q, scope, grant.Session); err != nil {
		return Project{}, err
	}
	var paths string
	var revision int64
	var download bool
	if err := q.QueryRowContext(ctx, `SELECT paths,revision,allow_download FROM conversation_grants WHERE reader_id=? AND session_id=? AND revoked=0 AND suspended=0`, scope.Actor, grant.Session).Scan(&paths, &revision, &download); err != nil {
		return Project{}, missing(err)
	}
	var selected []string
	if err := json.Unmarshal([]byte(paths), &selected); err != nil {
		return Project{}, err
	}
	if revision != grant.Revision || !slices.Contains(selected, grant.Path) {
		return Project{}, ErrNotFound
	}
	if grant.Download && !download {
		return Project{}, ErrForbidden
	}
	return scanProject(q.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, scope.ProjectID))
}
func fileReadAction(ctx context.Context, q queryer, scope Scope, action ProductAction) (Project, error) {
	if _, shared := ctx.Value(receivedFileKey{}).(receivedFileGrant); !shared {
		return projectAction(ctx, q, scope, action)
	}
	if action != ActionFilesRead {
		return Project{}, ErrForbidden
	}
	return fileReadProject(ctx, q, scope)
}
func (w *WorkspaceReader) readResources(ctx context.Context, scope Scope) (Resources, error) {
	if _, err := fileReadProject(ctx, w.Store.reader, scope); err != nil {
		return Resources{}, err
	}
	return w.Store.resourcesFor(ctx, scope.ProjectID)
}

// Carriers are host-owned, multiplexed across authorized readers. Their lifetime
// checks placement only; each individual read carries and rechecks its authority.
func (w *WorkspaceReader) activeReadResources(ctx context.Context, project string) (Resources, error) {
	p, err := scanProject(w.Store.reader.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, project))
	if err != nil {
		return Resources{}, err
	}
	if p.ExecutionSuspended {
		return Resources{}, ErrForbidden
	}
	if p.State != "active" {
		return Resources{}, ErrPending
	}
	return w.Store.resourcesFor(ctx, project)
}
func (f *Files) CaptureReceived(ctx context.Context, actor, session, path string, download bool) (*CapturedFile, error) {
	ctx, scope, err := f.Store.receivedFileContext(ctx, actor, session, path, download)
	if err != nil {
		return nil, err
	}
	return f.workspaceReader().Capture(ctx, scope, path)
}
