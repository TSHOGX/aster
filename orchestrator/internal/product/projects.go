package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const projectColumns = `id,owner_id,title,state,revision,activity_revision,coalesce(idle_deadline,''),failure,created_at,updated_at,execution_suspended,execution_hold_reason`
const sessionColumns = `id,project_id,title,state,deletion_intent,control_revision,revision,failure,created_at,updated_at`
const terminalColumns = `id,project_id,title,state,deletion_intent,revision,rows,cols,exit_code,failure,created_at,updated_at,coalesce((SELECT ti.id FROM terminal_incarnations ti WHERE ti.terminal_id=terminals.id ORDER BY ti.rowid DESC LIMIT 1),'')`

type scanner interface{ Scan(...any) error }

func scanProject(row scanner) (p Project, err error) {
	err = missing(row.Scan(&p.ID, &p.OwnerID, &p.Title, &p.State, &p.Revision, &p.ActivityRevision, &p.IdleDeadline, &p.Failure, &p.CreatedAt, &p.UpdatedAt, &p.ExecutionSuspended, &p.ExecutionHoldReason))
	return
}
func scanSession(row scanner) (p Session, err error) {
	err = missing(row.Scan(&p.ID, &p.ProjectID, &p.Title, &p.State, &p.Deletion, &p.ControlRevision, &p.Revision, &p.Failure, &p.CreatedAt, &p.UpdatedAt))
	return
}
func scanTerminal(row scanner) (p Terminal, err error) {
	err = missing(row.Scan(&p.ID, &p.ProjectID, &p.Title, &p.State, &p.Deletion, &p.Revision, &p.Rows, &p.Cols, &p.ExitCode, &p.Failure, &p.CreatedAt, &p.UpdatedAt, &p.Incarnation))
	return
}

func ownedProject(ctx context.Context, q queryer, scope Scope) (Project, error) {
	if scope.Actor == "" || scope.ProjectID == "" {
		return Project{}, ErrNotFound
	}
	return scanProject(q.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=? AND owner_id=?`, scope.ProjectID, scope.Actor))
}

// projectAction combines stored ownership with a current named grant. It must
// run in the admission transaction for writes. Ownership-only helpers are for
// already-admitted settlement and explicitly scoped resource operations.
func projectAction(ctx context.Context, q queryer, scope Scope, action ProductAction) (Project, error) {
	p, err := ownedProject(ctx, q, scope)
	if err != nil {
		return p, err
	}
	if action == ActionAgentsRun || action == ActionTerminalsUse {
		if err = executionAllowed(ctx, q, scope.ProjectID); err != nil {
			return Project{}, err
		}
	}
	if err = authorizeAction(ctx, q, scope.Actor, action); err != nil {
		return Project{}, err
	}
	return p, nil
}
func activeOwnedProject(ctx context.Context, q queryer, scope Scope) (Project, error) {
	p, err := ownedProject(ctx, q, scope)
	if err != nil {
		return p, err
	}
	if err = activeAccount(ctx, q, scope.Actor); err != nil {
		return Project{}, err
	}
	return p, nil
}
func mutableOwnedProject(ctx context.Context, q queryer, scope Scope) (Project, error) {
	p, err := ownedProject(ctx, q, scope)
	if err != nil {
		return p, err
	}
	if p.State == "deleting" || p.State == "deleted" {
		return p, ErrDeleting
	}
	return p, nil
}
func sessionIn(ctx context.Context, q queryer, scope Scope, id string) (Session, error) {
	if _, err := ownedProject(ctx, q, scope); err != nil {
		return Session{}, err
	}
	return scanSession(q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE project_id=? AND id=?`, scope.ProjectID, id))
}
func readableSession(ctx context.Context, q queryer, scope Scope, id string) (Session, error) {
	child, err := sessionIn(ctx, q, scope, id)
	if err != nil {
		return child, err
	}
	if err = authorizeAction(ctx, q, scope.Actor, ActionProjectsRead); err != nil {
		return Session{}, err
	}
	return child, nil
}
func terminalIn(ctx context.Context, q queryer, scope Scope, id string) (Terminal, error) {
	if _, err := ownedProject(ctx, q, scope); err != nil {
		return Terminal{}, err
	}
	return scanTerminal(q.QueryRowContext(ctx, `SELECT `+terminalColumns+` FROM terminals WHERE project_id=? AND id=?`, scope.ProjectID, id))
}
func (s *Store) event(ctx context.Context, tx *sql.Tx, project, session, terminal, kind string, revision int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO events(project_id,session_id,terminal_id,kind,revision,created_at) VALUES(?,nullif(?,''),nullif(?,''),?,?,?)`, project, session, terminal, kind, revision, s.timestamp())
	return err
}
func (s *Store) bumpProject(ctx context.Context, tx *sql.Tx, project, kind string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), project); err != nil {
		return err
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM projects WHERE id=?`, project).Scan(&rev); err != nil {
		return err
	}
	return s.event(ctx, tx, project, "", "", kind, rev)
}
func (s *Store) activity(ctx context.Context, tx *sql.Tx, project string) error {
	_, err := tx.ExecContext(ctx, `UPDATE projects SET activity_revision=activity_revision+1,idle_deadline=NULL WHERE id=?`, project)
	return err
}
func (s *Store) CreateProject(ctx context.Context, actor, id, title string) (Project, error) {
	if !validText(actor, 256) || !validID(id) || !validText(title, 256) {
		return Project{}, ErrInvalid
	}
	var p Project
	err := s.write(ctx, func(tx *sql.Tx) error {
		existing, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, id))
		if err == nil {
			if existing.OwnerID != actor {
				return ErrNotFound
			}
			if err := authorizeAction(ctx, tx, actor, ActionProjectsCreate); err != nil {
				return err
			}
			if !creationMatches(ctx, tx, "projects", id, title) {
				return ErrConflict
			}
			p = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := authorizeAction(ctx, tx, actor, ActionProjectsCreate); err != nil {
			return err
		}
		if err := admitCount(ctx, tx, actor, ResourceProjects, 1); err != nil {
			return err
		}
		now := s.timestamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id,owner_id,title,creation_digest,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, actor, title, creationDigest(title), now, now); err != nil {
			return err
		}
		if err := saveInitialConfiguration(ctx, tx, actor, id); err != nil {
			return err
		}
		if err := s.event(ctx, tx, id, "", "", "project.created", 1); err != nil {
			return err
		}
		p, err = ownedProject(ctx, tx, Scope{actor, id})
		return err
	})
	return p, err
}
func (s *Store) Project(ctx context.Context, scope Scope) (Project, error) {
	return projectAction(ctx, s.reader, scope, ActionProjectsRead)
}
func (s *Store) Session(ctx context.Context, scope Scope, id string) (Session, error) {
	return readableSession(ctx, s.reader, scope, id)
}
func (s *Store) Terminal(ctx context.Context, scope Scope, id string) (Terminal, error) {
	child, err := terminalIn(ctx, s.reader, scope, id)
	if err != nil {
		return child, err
	}
	if err = authorizeAction(ctx, s.reader, scope.Actor, ActionProjectsRead); err != nil {
		return Terminal{}, err
	}
	return child, nil
}
func (s *Store) Projects(ctx context.Context, actor, after string, limit int) ([]Project, error) {
	if actor == "" {
		return nil, ErrNotFound
	}
	if err := authorizeAction(ctx, s.reader, actor, ActionProjectsRead); err != nil {
		return nil, err
	}
	limit = pageSize(limit)
	rows, err := s.reader.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE owner_id=? AND id>? AND state<>'deleted' ORDER BY id LIMIT ?`, actor, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Sessions(ctx context.Context, scope Scope, after string, limit int) ([]Session, error) {
	if _, err := projectAction(ctx, s.reader, scope, ActionProjectsRead); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE project_id=? AND id>? AND deletion_intent<2 ORDER BY id LIMIT ?`, scope.ProjectID, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		p, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Terminals(ctx context.Context, scope Scope, after string, limit int) ([]Terminal, error) {
	if _, err := projectAction(ctx, s.reader, scope, ActionProjectsRead); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+terminalColumns+` FROM terminals WHERE project_id=? AND id>? AND deletion_intent<2 ORDER BY id LIMIT ?`, scope.ProjectID, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Terminal{}
	for rows.Next() {
		p, err := scanTerminal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) CreateSession(ctx context.Context, scope Scope, id, title string) (Session, error) {
	if !validID(id) || (title != "" && !validText(title, 256)) {
		return Session{}, ErrInvalid
	}
	var p Session
	err := s.write(ctx, func(tx *sql.Tx) error { var err error; p, err = s.createSession(ctx, tx, scope, id, title); return err })
	return p, err
}
func (s *Store) createSession(ctx context.Context, tx *sql.Tx, scope Scope, id, title string) (Session, error) {
	var p Session
	err := func() error {
		if _, err := projectAction(ctx, tx, scope, ActionAgentsRun); err != nil {
			return err
		}
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		existing, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=?`, id))
		if err == nil {
			if existing.ProjectID != scope.ProjectID {
				return ErrNotFound
			}
			if !creationMatches(ctx, tx, "sessions", id, title) {
				return ErrConflict
			}
			p = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := s.timestamp()
		display, source := title, "user"
		if title == "" {
			display, source = "New agent", "pending"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,project_id,title,title_source,creation_digest,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, scope.ProjectID, display, source, creationDigest(title), now, now); err != nil {
			return err
		}
		if err := s.event(ctx, tx, scope.ProjectID, id, "", "session.created", 1); err != nil {
			return err
		}
		if err := s.bumpProject(ctx, tx, scope.ProjectID, "project.children"); err != nil {
			return err
		}
		p, err = sessionIn(ctx, tx, scope, id)
		return err
	}()
	return p, err
}
func (s *Store) CreateTerminal(ctx context.Context, scope Scope, id, title string, rows, cols int) (Terminal, error) {
	if !validID(id) || !validText(title, 256) || rows < 1 || rows > 500 || cols < 1 || cols > 1000 {
		return Terminal{}, ErrInvalid
	}
	var p Terminal
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := projectAction(ctx, tx, scope, ActionTerminalsUse); err != nil {
			return err
		}
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		existing, err := scanTerminal(tx.QueryRowContext(ctx, `SELECT `+terminalColumns+` FROM terminals WHERE id=?`, id))
		if err == nil {
			if existing.ProjectID != scope.ProjectID {
				return ErrNotFound
			}
			if !creationMatches(ctx, tx, "terminals", id, title, rows, cols) {
				return ErrConflict
			}
			p = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := s.timestamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO terminals(id,project_id,title,creation_digest,rows,cols,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, scope.ProjectID, title, creationDigest(title, rows, cols), rows, cols, now, now); err != nil {
			return err
		}
		if err := s.event(ctx, tx, scope.ProjectID, "", id, "terminal.created", 1); err != nil {
			return err
		}
		if err := s.bumpProject(ctx, tx, scope.ProjectID, "project.children"); err != nil {
			return err
		}
		p, err = terminalIn(ctx, tx, scope, id)
		return err
	})
	return p, err
}
func pageSize(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

// Rename* compares the revision of the edited entity, not a parent or sibling.
func (s *Store) RenameProject(ctx context.Context, scope Scope, revision int64, title string) error {
	if !validText(title, 256) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := projectAction(ctx, tx, scope, ActionProjectsManage); err != nil {
			return err
		}
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE projects SET title=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, title, s.timestamp(), scope.ProjectID, revision)); err != nil {
			return err
		}
		return s.event(ctx, tx, scope.ProjectID, "", "", "project.updated", revision+1)
	})
}
func (s *Store) RenameSession(ctx context.Context, scope Scope, id string, revision int64, title string) error {
	return s.editChild(ctx, scope, id, "session", revision, title, false)
}
func (s *Store) RenameTerminal(ctx context.Context, scope Scope, id string, revision int64, title string) error {
	return s.editChild(ctx, scope, id, "terminal", revision, title, false)
}
func (s *Store) DeleteSession(ctx context.Context, scope Scope, id string, revision int64) error {
	return s.editChild(ctx, scope, id, "session", revision, "", true)
}
func (s *Store) DeleteTerminal(ctx context.Context, scope Scope, id string, revision int64) error {
	return s.editChild(ctx, scope, id, "terminal", revision, "", true)
}
func (s *Store) editChild(ctx context.Context, scope Scope, id, kind string, revision int64, title string, deleting bool) error {
	if !deleting && !validText(title, 256) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		var deletion Deletion
		var rev int64
		if kind == "session" {
			p, err := sessionIn(ctx, tx, scope, id)
			if err != nil {
				return err
			}
			deletion, rev = p.Deletion, p.Revision
		} else {
			p, err := terminalIn(ctx, tx, scope, id)
			if err != nil {
				return err
			}
			deletion, rev = p.Deletion, p.Revision
		}
		if err := activeAccount(ctx, tx, scope.Actor); err != nil {
			return err
		}
		if !deleting {
			if err := authorizeAction(ctx, tx, scope.Actor, ActionProjectsManage); err != nil {
				return err
			}
		}
		if deletion != Live {
			return ErrDeleting
		}
		if revision != rev {
			return ErrConflict
		}
		table := kind + "s" // kind is an internal constant, never user input.
		// Deletion records intent in its own column. The child's execution state is
		// left alone, so how it was running, blocked or exited stays visible while
		// cleanup runs and after it finishes.
		field, value := "title", any(title)
		if deleting {
			field, value = "deletion_intent", any(int(Requested))
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE `+table+` SET `+field+`=?,revision=revision+1,updated_at=? WHERE id=? AND project_id=? AND revision=?`, value, s.timestamp(), id, scope.ProjectID, revision)); err != nil {
			return err
		}
		if kind == "session" && !deleting {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET title_source='user' WHERE id=?`, id); err != nil {
				return err
			}
		}
		session, terminal := id, ""
		if kind == "terminal" {
			session, terminal = "", id
		}
		eventKind := kind + ".updated"
		if deleting {
			eventKind = kind + ".deleting"
			if err := s.activity(ctx, tx, scope.ProjectID); err != nil {
				return err
			}
		}
		return s.event(ctx, tx, scope.ProjectID, session, terminal, eventKind, revision+1)
	})
}
func (s *Store) DeleteProject(ctx context.Context, scope Scope, revision int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := activeOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE projects SET state='deleting',revision=revision+1,activity_revision=activity_revision+1,idle_deadline=NULL,updated_at=? WHERE id=? AND revision=?`, s.timestamp(), scope.ProjectID, revision)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET disposition='canceled',updated_at=? WHERE project_id=? AND disposition='pending'`, s.timestamp(), scope.ProjectID); err != nil {
			return err
		}
		return s.event(ctx, tx, scope.ProjectID, "", "", "project.deleting", revision+1)
	})
}

// Retried create intents are compared with the original payload, even after a
// rename or resize. A replay must never undo later edits.
func creationDigest(values ...any) string {
	b, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func creationMatches(ctx context.Context, q queryer, table, id string, values ...any) bool {
	var digest string
	err := q.QueryRowContext(ctx, `SELECT creation_digest FROM `+table+` WHERE id=?`, id).Scan(&digest)
	return err == nil && digest == creationDigest(values...)
}
