package product

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sandbox"
)

type AgentRecovery struct {
	Store        *Store
	Lifecycle    *Lifecycle
	Backend      sandbox.Processes
	RunnerBinary string
}

func (r *AgentRecovery) Step(ctx context.Context, limit int) error {
	if limit < 1 || limit > 16 {
		limit = 8
	}
	rows, err := r.Store.reader.QueryContext(ctx, `SELECT i.id FROM agent_incarnations i JOIN projects p ON p.id=i.project_id WHERE i.state='exited' AND i.recovery_state IN ('pending','replaying') AND p.state NOT IN ('deleting','deleted') ORDER BY i.recovery_attempted,i.id LIMIT ?`, limit)
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
	var failures []error
	for _, id := range ids {
		attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
		failures = append(failures, r.recover(attempt, id))
		cancel()
	}
	return errors.Join(failures...)
}
func (r *AgentRecovery) recover(ctx context.Context, id string) error {
	releaseMaintenance, admissionErr := r.Store.beginRuntimeWork(ctx, runtimeMaintenance)
	if admissionErr != nil {
		return admissionErr
	}
	defer releaseMaintenance()

	var i Incarnation
	err := r.Store.write(ctx, func(tx *sql.Tx) error {
		var err error
		i, err = scanIncarnation(tx.QueryRowContext(ctx, `SELECT `+incarnationColumns+` FROM agent_incarnations WHERE id=? AND state='exited' AND recovery_state IN ('pending','replaying')`, id))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_incarnations SET recovery_state='replaying',owner=owner+1,recovery_attempted=? WHERE id=?`, r.Store.timestamp(), id); err != nil {
			return err
		}
		i.Owner++
		return nil
	})
	if err != nil {
		return err
	}
	var actor string
	if err = r.Store.reader.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=? AND state NOT IN ('deleting','deleted')`, i.ProjectID).Scan(&actor); err != nil {
		return missing(err)
	}
	resources, err := r.Store.resourcesFor(ctx, i.ProjectID)
	if err != nil {
		return err
	}
	scope := Scope{actor, i.ProjectID}
	if err = r.Lifecycle.EnsureActive(ctx, scope, resources.Profile); err != nil {
		return err
	}
	resources, err = r.Lifecycle.ActiveResources(ctx, scope)
	if err != nil {
		return err
	}
	helper := r.RunnerBinary
	if helper == "" {
		helper = "/usr/local/bin/agent-runner"
	}
	ref := sandbox.ProcessReference{StoreID: resources.Reference.StoreID, SandboxID: resources.Reference.SandboxID, Epoch: resources.Reference.Epoch, Handle: "agent-replay-" + uuid.NewString()}
	stream, err := r.Backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: ref, IdempotencyKey: ref.Handle, Command: helper, Arguments: []string{"--replay", "--session", i.SessionID, "--incarnation", i.ID, "--owner", strconv.FormatUint(i.Owner, 10), "--after", strconv.FormatUint(i.Ingested, 10)}, Directory: "/workspace"})
	if err != nil {
		return r.failure(ctx, i, err)
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	reader, writer := io.Pipe()
	defer reader.Close()
	pump := make(chan error, 1)
	var diagnostic strings.Builder
	go func() {
		defer writer.Close()
		for {
			f, e := stream.Next()
			if e != nil {
				_ = writer.CloseWithError(e)
				pump <- e
				return
			}
			if f.Gap || f.ResetReason != "" {
				e = errors.New("Agent journal recovery transport gap")
				_ = writer.CloseWithError(e)
				pump <- e
				return
			}
			if f.Kind == "end" {
				if f.ExitCode != 0 {
					e = &sandbox.ProcessExitError{Code: f.ExitCode, Diagnostic: diagnostic.String()}
					_ = writer.CloseWithError(e)
				}
				pump <- e
				return
			}
			if f.Kind == "data" && f.Stream == "stderr" && diagnostic.Len() < 2048 {
				n := min(len(f.Data), 2048-diagnostic.Len())
				diagnostic.Write(f.Data[:n])
			}
			if f.Kind == "data" && f.Stream != "stderr" {
				if _, e = writer.Write(f.Data); e != nil {
					pump <- e
					return
				}
			}
		}
	}()
	defer func() { reader.Close(); stream.Close(); <-pump }()
	decoder := agentwire.NewDecoder(reader)
	var recovered bool
	for {
		frame, e := decoder.Next()
		if e != nil {
			if e == io.EOF && recovered {
				return r.finish(ctx, i)
			}
			return r.failure(ctx, i, e)
		}
		if frame.Session != i.SessionID || frame.Incarnation != i.ID || frame.Owner != i.Owner {
			return r.failure(ctx, i, ErrConflict)
		}
		switch frame.Kind {
		case "record":
			if _, e = r.Store.ingestAgent(ctx, i, frame, true); e != nil {
				return r.failure(ctx, i, e)
			}
		case "recovered":
			var position uint64
			if e = r.Store.reader.QueryRowContext(ctx, `SELECT ingested_sequence FROM agent_incarnations WHERE id=? AND owner=?`, i.ID, i.Owner).Scan(&position); e != nil {
				return r.failure(ctx, i, e)
			}
			if position != frame.After {
				return r.failure(ctx, i, ErrConflict)
			}
			recovered = true
		default:
			return r.failure(ctx, i, ErrInvalid)
		}
	}
}
func (r *AgentRecovery) failure(ctx context.Context, i Incarnation, cause error) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	err := r.Store.write(settle, func(tx *sql.Tx) error {
		var exit *sandbox.ProcessExitError
		if errors.As(cause, &exit) && exit.Code == 12 {
			reason := cause.Error()
			if len(reason) > 1024 {
				reason = reason[:1024]
			}
			if _, err := tx.ExecContext(settle, `UPDATE agent_incarnations SET recovery_state='failed',error=? WHERE id=? AND owner=? AND recovery_state='replaying'`, reason, i.ID, i.Owner); err != nil {
				return err
			}
			if _, err := tx.ExecContext(settle, `UPDATE project_activity SET state='complete',updated_at=? WHERE id=?`, r.Store.timestamp(), "recovery/"+i.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(settle, `UPDATE sessions SET state='blocked',failure='original journal unavailable; retry recovery or deliberately continue from native history' WHERE id=? AND deletion_intent<2`, i.SessionID); err != nil {
				return err
			}
			return r.Store.bumpSession(settle, tx, i.ProjectID, i.SessionID, "session.recovery_failed")
		}
		_, err := tx.ExecContext(settle, `UPDATE agent_incarnations SET error=? WHERE id=? AND owner=? AND recovery_state='replaying'`, cause.Error(), i.ID, i.Owner)
		return err
	})
	return errors.Join(cause, err)
}
func (r *AgentRecovery) finish(ctx context.Context, i Incarnation) error {
	return r.Store.write(ctx, func(tx *sql.Tx) error {
		if err := changed(tx.ExecContext(ctx, `UPDATE agent_incarnations SET recovery_state='complete',error='' WHERE id=? AND owner=? AND recovery_state='replaying'`, i.ID, i.Owner)); err != nil {
			return err
		}
		var unknown, unsettled int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND state='unknown' AND acknowledged=0`, i.SessionID).Scan(&unknown); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_activity WHERE session_id=? AND kind='descendants' AND state<>'complete'`, i.SessionID).Scan(&unsettled); err != nil {
			return err
		}
		state, failure := "idle", ""
		if unknown > 0 || unsettled > 0 {
			state, failure = "blocked", "native outcome or descendant cleanup remains unknown; inspect recovery details"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state=?,failure=? WHERE id=? AND deletion_intent<2`, state, failure, i.SessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_activity SET state='complete',updated_at=? WHERE id=?`, r.Store.timestamp(), "recovery/"+i.ID); err != nil {
			return err
		}
		if err := r.Store.activity(ctx, tx, i.ProjectID); err != nil {
			return err
		}
		return r.Store.bumpSession(ctx, tx, i.ProjectID, i.SessionID, "session.reconciled")
	})
}

// ContinueSession is deliberate acknowledgement of an unresolved prior outcome.
// It never changes that turn to success/failure and never reissues its command.
func (s *Store) ContinueSession(ctx context.Context, scope Scope, session string, revision int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.State != "blocked" || child.Revision != revision || child.Deletion != Live {
			return ErrConflict
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM agent_incarnations WHERE session_id=? AND (state<>'exited' OR recovery_state IN ('pending','replaying'))) + (SELECT count(*) FROM project_activity WHERE session_id=? AND kind IN ('descendants','agent_control') AND state<>'complete')`, session, session).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrBusy
		}
		if _, err = tx.ExecContext(ctx, `UPDATE turns SET acknowledged=1 WHERE session_id=? AND state='unknown'`, session); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE turns SET state='held',updated_at=? WHERE session_id=? AND state='queued'`, s.timestamp(), session); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state='idle',failure='' WHERE id=?`, session); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "session.continued")
	})
}

func (s *Store) RetryRecovery(ctx context.Context, scope Scope, session string, revision int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := writableProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Revision != revision || child.Deletion != Live {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE project_activity SET state='active',updated_at=? WHERE id IN (SELECT 'recovery/'||id FROM agent_incarnations WHERE session_id=? AND recovery_state='failed')`, s.timestamp(), session); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_incarnations SET recovery_state='pending',error='' WHERE session_id=? AND recovery_state='failed'`, session); err != nil {
			return err
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "session.recovery_requested")
	})
}
