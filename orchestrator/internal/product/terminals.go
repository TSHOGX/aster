package product

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
)

// TerminalIncarnation is one PTY process of a Terminal. The Terminal keeps its
// identity across incarnations; this record identifies a particular execution,
// and it is the only thing that pins Project activity for a terminal.
type TerminalIncarnation struct {
	ID, ProjectID, TerminalID string
	Reference                 sandbox.ProcessReference
	Owner                     uint64
	State                     string
	Rows, Cols                int
	ExitCode                  *int
	Error                     string
}

const terminalIncarnationColumns = `id,project_id,terminal_id,store_id,sandbox_id,epoch,handle,owner,state,rows,cols,exit_code,error`

func scanTerminalIncarnation(row scanner) (i TerminalIncarnation, err error) {
	err = missing(row.Scan(&i.ID, &i.ProjectID, &i.TerminalID, &i.Reference.StoreID, &i.Reference.SandboxID, &i.Reference.Epoch, &i.Reference.Handle, &i.Owner, &i.State, &i.Rows, &i.Cols, &i.ExitCode, &i.Error))
	return
}

func liveTerminalIncarnation(ctx context.Context, q queryer, terminal string) (TerminalIncarnation, error) {
	return scanTerminalIncarnation(q.QueryRowContext(ctx, `SELECT `+terminalIncarnationColumns+` FROM terminal_incarnations WHERE terminal_id=? AND state IN ('starting','running')`, terminal))
}

// LiveTerminal returns the Terminal's current PTY process, or ErrNotFound when it
// has none. A Terminal row without a live incarnation is a terminal that is not
// running: that is an ordinary state, not a fault.
func (s *Store) LiveTerminal(ctx context.Context, scope Scope, terminal string) (TerminalIncarnation, error) {
	if _, err := terminalIn(ctx, s.reader, scope, terminal); err != nil {
		return TerminalIncarnation{}, err
	}
	return liveTerminalIncarnation(ctx, s.reader, terminal)
}

// reserveTerminal records the intent to run a PTY before any remote effect, and
// returns the existing live incarnation when one is already recorded. Starting a
// terminal borrows the Project's placement; it never reserves capacity of its own.
func (s *Store) reserveTerminal(ctx context.Context, scope Scope, terminal string, rows, cols int) (i TerminalIncarnation, err error) {
	if rows < 1 || rows > 500 || cols < 1 || cols > 1000 {
		return i, ErrInvalid
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		p, err := writableProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if p.State != "active" {
			return ErrPending
		}
		child, err := terminalIn(ctx, tx, scope, terminal)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		i, err = liveTerminalIncarnation(ctx, tx, terminal)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		r, err := resources(ctx, tx, scope.ProjectID)
		if err != nil {
			return err
		}
		id := uuid.NewString()
		i = TerminalIncarnation{ID: id, ProjectID: scope.ProjectID, TerminalID: terminal, Reference: sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: "terminal-" + id}, Owner: 1, State: "starting", Rows: rows, Cols: cols}
		if _, err = tx.ExecContext(ctx, `INSERT INTO terminal_incarnations(id,project_id,terminal_id,store_id,sandbox_id,epoch,handle,owner,state,rows,cols,created_at) VALUES(?,?,?,?,?,?,?,1,'starting',?,?,?)`, id, scope.ProjectID, terminal, i.Reference.StoreID, i.Reference.SandboxID, i.Reference.Epoch, i.Reference.Handle, rows, cols, s.timestamp()); err != nil {
			return err
		}
		// The requested size becomes the Terminal's persistent size, so a later
		// incarnation starts at the dimensions the user last chose.
		if _, err = tx.ExecContext(ctx, `UPDATE terminals SET state='starting',rows=?,cols=?,exit_code=NULL,failure='' WHERE id=?`, rows, cols, terminal); err != nil {
			return err
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpTerminal(ctx, tx, scope.ProjectID, terminal, "terminal.starting")
	})
	return
}

func (s *Store) bumpTerminal(ctx context.Context, tx *sql.Tx, project, terminal, kind string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE terminals SET revision=revision+1,updated_at=? WHERE id=? AND project_id=?`, s.timestamp(), terminal, project); err != nil {
		return err
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM terminals WHERE id=? AND project_id=?`, terminal, project).Scan(&rev); err != nil {
		return err
	}
	return s.event(ctx, tx, project, "", terminal, kind, rev)
}

// takeTerminalOwnership fences a previous application owner out of this PTY. An
// old owner's input, resize and exit writes are rejected afterwards, so a
// takeover cannot have two writers driving one keyboard.
func (s *Store) takeTerminalOwnership(ctx context.Context, i TerminalIncarnation) (TerminalIncarnation, error) {
	err := s.write(ctx, func(tx *sql.Tx) error {
		current, err := liveTerminalIncarnation(ctx, tx, i.TerminalID)
		if err != nil {
			return err
		}
		if current.ID != i.ID {
			return ErrConflict
		}
		return tx.QueryRowContext(ctx, `UPDATE terminal_incarnations SET owner=owner+1 WHERE id=? RETURNING owner`, i.ID).Scan(&i.Owner)
	})
	return i, err
}

// TerminalRunning records that the PTY's start frame arrived. It is fenced on the
// owner so a stale attachment cannot revive an incarnation the store has moved on
// from.
func (s *Store) TerminalRunning(ctx context.Context, i TerminalIncarnation) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := changed(tx.ExecContext(ctx, `UPDATE terminal_incarnations SET state='running',error='' WHERE id=? AND owner=? AND state IN ('starting','running')`, i.ID, i.Owner)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE terminals SET state='running' WHERE id=? AND state='starting'`, i.TerminalID); err != nil {
			return err
		}
		return s.bumpTerminal(ctx, tx, i.ProjectID, i.TerminalID, "terminal.running")
	})
}

// ResizeTerminal converges to the latest requested dimensions. It persists them on
// the Terminal so a later incarnation starts correctly sized, and reports whether
// a live process must be told. Resize is not a product event: dimensions travel on
// the attachment, and a resize storm must not flood the outbox.
func (s *Store) ResizeTerminal(ctx context.Context, scope Scope, terminal string, rows, cols int) (TerminalIncarnation, error) {
	return s.resizeTerminal(ctx, scope, terminal, rows, cols, nil)
}
func (s *Store) resizeTerminal(ctx context.Context, scope Scope, terminal string, rows, cols int, expected *TerminalIncarnation) (TerminalIncarnation, error) {
	if rows < 1 || rows > 500 || cols < 1 || cols > 1000 {
		return TerminalIncarnation{}, ErrInvalid
	}
	var i TerminalIncarnation
	err := s.write(ctx, func(tx *sql.Tx) error {
		child, err := terminalIn(ctx, tx, scope, terminal)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		if expected != nil {
			current, e := liveTerminalIncarnation(ctx, tx, terminal)
			if e != nil {
				return e
			}
			if current.ID != expected.ID || current.Owner != expected.Owner {
				return ErrConflict
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE terminals SET rows=?,cols=?,updated_at=? WHERE id=? AND project_id=?`, rows, cols, s.timestamp(), terminal, scope.ProjectID); err != nil {
			return err
		}
		i, err = liveTerminalIncarnation(ctx, tx, terminal)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		i.Rows, i.Cols = rows, cols
		_, err = tx.ExecContext(ctx, `UPDATE terminal_incarnations SET rows=?,cols=? WHERE id=? AND owner=?`, rows, cols, i.ID, i.Owner)
		return err
	})
	return i, err
}

// SettleTerminalExit records a PTY's observed exit. The code comes only from the
// process stream's terminal frame; a transport loss or a missing handle is never
// allowed to manufacture one. It is a worker operation with no actor scope,
// because an exit must still be recorded after deletion closed user admission.
func (s *Store) SettleTerminalExit(ctx context.Context, i TerminalIncarnation, code int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := changed(tx.ExecContext(ctx, `UPDATE terminal_incarnations SET state='exited',exit_code=?,error='' WHERE id=? AND owner=? AND state IN ('starting','running')`, code, i.ID, i.Owner)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE terminals SET state='exited',exit_code=? WHERE id=? AND project_id=?`, code, i.TerminalID, i.ProjectID); err != nil {
			return err
		}
		if err := s.bumpTerminal(ctx, tx, i.ProjectID, i.TerminalID, "terminal.exited"); err != nil {
			return err
		}
		// The PTY no longer pins the Project. This never pauses anything by itself.
		return s.activity(ctx, tx, i.ProjectID)
	})
}

// LoseTerminal records that the placement holding a PTY is gone. The process did
// not exit: its outcome is unobservable, so no exit code is recorded and the
// reason is explicit. Reopening the Terminal is a new incarnation, never a claim
// that this process continued.
func (s *Store) LoseTerminal(ctx context.Context, i TerminalIncarnation, reason string) error {
	if reason == "" {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := changed(tx.ExecContext(ctx, `UPDATE terminal_incarnations SET state='lost',error=? WHERE id=? AND owner=? AND state IN ('starting','running')`, reason, i.ID, i.Owner)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE terminals SET state='lost',exit_code=NULL,failure=? WHERE id=? AND project_id=?`, reason, i.TerminalID, i.ProjectID); err != nil {
			return err
		}
		if err := s.bumpTerminal(ctx, tx, i.ProjectID, i.TerminalID, "terminal.lost"); err != nil {
			return err
		}
		return s.activity(ctx, tx, i.ProjectID)
	})
}

// LiveTerminals lists a Project's recorded PTY processes for recovery and
// operator diagnostics. A worker uses it to reconcile after restart: each row is
// a process the application believed it owned.
func (s *Store) LiveTerminals(ctx context.Context, project string, limit int) ([]TerminalIncarnation, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+terminalIncarnationColumns+` FROM terminal_incarnations WHERE project_id=? AND state IN ('starting','running') ORDER BY created_at,id LIMIT ?`, project, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TerminalIncarnation{}
	for rows.Next() {
		i, err := scanTerminalIncarnation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// LoseTerminalsForPlacement settles every PTY an obsolete placement held. Khora
// pause removes a placement and resume cold-starts another epoch, so those
// processes are gone; the application says so explicitly rather than leaving rows
// that pin an idle Project forever. Terminals whose recorded epoch matches the
// current placement are left alone.
func (s *Store) LoseTerminalsForPlacement(ctx context.Context, project string, epoch uint64, reason string) (int, error) {
	if reason == "" {
		return 0, ErrInvalid
	}
	settled := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		settled, err = s.loseStalePTYs(ctx, tx, project, epoch, reason)
		return err
	})
	return settled, err
}

// loseStalePTYs settles PTY records whose placement epoch is no longer current.
// The lifecycle calls it in the same transaction that commits a new epoch, so a
// replaced placement can never leave a running PTY row pinning an idle Project
// even for the window before a worker notices.
func (s *Store) loseStalePTYs(ctx context.Context, tx *sql.Tx, project string, epoch uint64, reason string) (int, error) {
	settled := 0
	err := func() error {
		rows, err := tx.QueryContext(ctx, `SELECT `+terminalIncarnationColumns+` FROM terminal_incarnations WHERE project_id=? AND state IN ('starting','running') AND epoch<>?`, project, epoch)
		if err != nil {
			return err
		}
		var stale []TerminalIncarnation
		for rows.Next() {
			i, err := scanTerminalIncarnation(rows)
			if err != nil {
				rows.Close()
				return err
			}
			stale = append(stale, i)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, i := range stale {
			if _, err = tx.ExecContext(ctx, `UPDATE terminal_incarnations SET state='lost',error=? WHERE id=?`, reason, i.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE terminals SET state='lost',exit_code=NULL,failure=? WHERE id=? AND project_id=?`, reason, i.TerminalID, project); err != nil {
				return err
			}
			if err = s.bumpTerminal(ctx, tx, project, i.TerminalID, "terminal.lost"); err != nil {
				return err
			}
			settled++
		}
		if settled > 0 {
			return s.activity(ctx, tx, project)
		}
		return nil
	}()
	return settled, err
}

func (s *Store) terminalAuthority(ctx context.Context, i TerminalIncarnation) error {
	var found string
	return missing(s.reader.QueryRowContext(ctx, `SELECT i.id FROM terminal_incarnations i JOIN terminals t ON t.id=i.terminal_id JOIN project_resources r ON r.project_id=i.project_id JOIN projects p ON p.id=i.project_id WHERE i.id=? AND i.owner=? AND i.state IN ('starting','running') AND t.deletion_intent=0 AND p.state='active' AND r.store_id=i.store_id AND r.sandbox_id=i.sandbox_id AND r.epoch=i.epoch`, i.ID, i.Owner).Scan(&found))
}
