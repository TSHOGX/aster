package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const adminProjectActions = `'project.pause','project.stop','project.restart','project.apply_network'`

type AdminProjectControl struct {
	RequestID                string `json:"request_id"`
	ExpectedRevision         int64  `json:"expected_revision"`
	ExpectedNetworkRevision  string `json:"expected_network_revision,omitempty"`
	InterruptionAcknowledged bool   `json:"interruption_acknowledged"`
}
type AdminProjectControlResult struct {
	Operation AdminOperation `json:"operation"`
	Replayed  bool           `json:"replayed"`
}

func noAdminProjectControl(ctx context.Context, q queryer, project string) error {
	var pending bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_operations WHERE target_kind='project' AND target_id=? AND action IN (`+adminProjectActions+`) AND state NOT IN ('succeeded','failed'))`, project).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return Admission("admin_operation_pending", ErrConflict)
	}
	return nil
}
func (s *Store) AdminControlProject(ctx context.Context, token, project, action string, input AdminProjectControl) (AdminProjectControlResult, error) {
	var out AdminProjectControlResult
	if action != "pause" && action != "stop" && action != "restart" && action != "apply_network" {
		return out, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "project."+action, project, input)
	if err != nil {
		return out, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		raw, exists, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if exists {
			if err = json.Unmarshal(raw, &out); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		if !input.InterruptionAcknowledged || !validID(project) || input.ExpectedRevision < 1 {
			return ErrInvalid
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return ErrDraining
		}
		p, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, project))
		if err != nil {
			return err
		}
		if p.State == "deleting" || p.State == "deleted" || p.Revision != input.ExpectedRevision {
			return ErrConflict
		}
		if err = noAdminProjectControl(ctx, tx, project); err != nil {
			return err
		}
		var pendingSuspension bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_operation_targets t JOIN admin_operations o ON o.id=t.operation_id WHERE t.project_id=? AND o.action='account.suspend' AND t.state<>'succeeded')`, project).Scan(&pendingSuspension); err != nil {
			return err
		}
		if pendingSuspension {
			return Admission("suspension_pending", ErrConflict)
		}
		if action == "restart" || action == "apply_network" {
			if err = activeAccount(ctx, tx, p.OwnerID); err != nil {
				return err
			}
			r, err := resources(ctx, tx, project)
			if err != nil {
				return err
			}
			if err = validateRetainedProfile(ctx, tx, r.Profile); err != nil {
				return err
			}
			if err = admitCompute(ctx, tx, p.OwnerID, r.Profile, r.Occupied); err != nil {
				return err
			}
		}
		network, err := projectNetworkSettings(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if action == "apply_network" {
			if input.ExpectedNetworkRevision == "" || input.ExpectedNetworkRevision != network.Desired.Revision {
				return Admission("network_revision_conflict", ErrConflict)
			}
		} else if input.ExpectedNetworkRevision != "" {
			return ErrInvalid
		}
		reason := "admin_" + action
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET execution_suspended=1,execution_hold_reason=? WHERE id=?`, reason, project); err != nil {
			return err
		}
		if err = s.holdProjectQueue(ctx, tx, project); err != nil {
			return err
		}
		if err = s.bumpProject(ctx, tx, project, "project.admin_control"); err != nil {
			return err
		}
		now := s.timestamp()
		out.Operation = AdminOperation{ID: uuid.NewString(), ActorID: actor.ID, Action: request.Action, TargetKind: "project", TargetID: project, State: "accepted", Total: 1, CreatedAt: now, UpdatedAt: now}
		if _, err = tx.ExecContext(ctx, `INSERT INTO admin_operations(id,actor_id,action,target_kind,target_id,state,created_at,updated_at) VALUES(?,?,?,'project',?,'accepted',?,?)`, out.Operation.ID, actor.ID, request.Action, project, now, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO admin_operation_targets(operation_id,project_id,phase,network_revision) VALUES(?,?,'stopping',?)`, out.Operation.ID, project, input.ExpectedNetworkRevision); err != nil {
			return err
		}
		if err = s.auditEvent(ctx, tx, actor.ID, request.Action, "project", project, request.ID, p.Revision+1, map[string]any{"operation_id": out.Operation.ID, "interruption_acknowledged": true, "desired_network_revision": network.Desired.Revision}); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		return s.writeAdminReceipt(ctx, tx, actor.ID, request, raw)
	})
	if err != nil {
		return AdminProjectControlResult{}, err
	}
	return out, nil
}
func (s *Store) holdProjectQueue(ctx context.Context, tx *sql.Tx, project string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM sessions WHERE project_id=? AND deletion_intent=0`, project)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.rejectAgentQueue(ctx, tx, project, id); err != nil {
			return err
		}
	}
	return nil
}

type adminProjectContinuation struct{ id, actor, project, action, phase, lifecycle, network string }

func adminProjectContinuationFor(ctx context.Context, q queryer, project string) (out adminProjectContinuation, err error) {
	err = missing(q.QueryRowContext(ctx, `SELECT o.id,o.actor_id,o.target_id,o.action,t.phase,t.lifecycle_id,t.network_revision FROM admin_operations o JOIN admin_operation_targets t ON t.operation_id=o.id AND t.project_id=o.target_id WHERE o.target_kind='project' AND o.target_id=? AND o.action IN (`+adminProjectActions+`) AND o.state NOT IN ('succeeded','failed') ORDER BY o.created_at LIMIT 1`, project).Scan(&out.id, &out.actor, &out.project, &out.action, &out.phase, &out.lifecycle, &out.network))
	return
}
func (s *Store) adminProjectOutcome(ctx context.Context, tx *sql.Tx, o adminProjectContinuation, state, detail string) error {
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	result, err := tx.ExecContext(ctx, `UPDATE admin_operations SET state=?,error=?,updated_at=? WHERE id=? AND state NOT IN ('succeeded','failed')`, state, detail, s.timestamp(), o.id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE admin_operation_targets SET state=?,error=? WHERE operation_id=?`, state, detail, o.id); err != nil {
		return err
	}
	if state == "succeeded" || state == "failed" {
		return s.auditEvent(ctx, tx, o.actor, o.action+".completed", "project", o.project, "", 0, map[string]any{"operation_id": o.id, "state": state, "error": detail})
	}
	return nil
}
func (s *Store) supersedeAdminProjectControls(ctx context.Context, tx *sql.Tx, account string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,actor_id,target_id,action FROM admin_operations WHERE target_kind='project' AND target_id IN (SELECT id FROM projects WHERE owner_id=?) AND action IN (`+adminProjectActions+`) AND state NOT IN ('succeeded','failed')`, account)
	if err != nil {
		return err
	}
	values := []adminProjectContinuation{}
	for rows.Next() {
		var o adminProjectContinuation
		if err = rows.Scan(&o.id, &o.actor, &o.project, &o.action); err != nil {
			break
		}
		values = append(values, o)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, o := range values {
		if err = s.adminProjectOutcome(ctx, tx, o, "failed", "superseded by account suspension"); err != nil {
			return err
		}
	}
	return nil
}
func (w *Workers) adminProjectStep(ctx context.Context, p Project) (bool, error) {
	o, err := adminProjectContinuationFor(ctx, w.Store.reader, p.ID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	outcome := func(state, detail string) error {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return w.Store.write(settle, func(tx *sql.Tx) error { return w.Store.adminProjectOutcome(settle, tx, o, state, detail) })
	}
	if p.State == "deleting" || p.State == "deleted" {
		return false, outcome("failed", "superseded by Project deletion")
	}
	if o.phase == "stopping" || o.phase == "failing" {
		if err = w.stopProjectChildren(ctx, p.ID); err == nil {
			err = w.Lifecycle.Reconcile(ctx, p.ID)
		}
		if err != nil {
			return true, errors.Join(err, outcome("unknown", err.Error()))
		}
		if o.phase == "failing" {
			return true, outcome("failed", "resume authority or owner quota changed; placement stopped")
		}
		if o.action == "project.pause" || o.action == "project.stop" {
			return true, outcome("succeeded", "")
		}
		err = w.Store.write(ctx, func(tx *sql.Tx) error {
			current, e := adminProjectContinuationFor(ctx, tx, p.ID)
			if e != nil {
				return e
			}
			if current.id != o.id || current.phase != "stopping" {
				return ErrConflict
			}
			actor, e := readAccount(ctx, tx, "id", o.actor)
			if e != nil {
				return e
			}
			if actor.Status != "active" || actor.Role != "admin" {
				return ErrForbidden
			}
			project, e := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, p.ID))
			if e != nil {
				return e
			}
			if e = activeAccount(ctx, tx, project.OwnerID); e != nil {
				return e
			}
			if project.State != "paused" && project.State != "created" {
				return ErrConflict
			}
			r, e := resources(ctx, tx, p.ID)
			if e != nil {
				return e
			}
			if o.network != "" {
				settings, e := projectNetworkSettings(ctx, tx, project.ID)
				if e != nil {
					return e
				}
				if settings.Desired.Revision != o.network {
					return Admission("network_revision_conflict", ErrConflict)
				}
			}
			if _, e = tx.ExecContext(ctx, `UPDATE projects SET execution_suspended=0,execution_hold_reason='' WHERE id=?`, p.ID); e != nil {
				return e
			}
			if e = w.Lifecycle.admitProjectActivation(ctx, tx, project, r.Profile.Bindings()); e != nil {
				return e
			}
			intent, e := pendingOperation(ctx, tx, p.ID)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE admin_operation_targets SET phase='resuming',lifecycle_id=?,state='running',error='' WHERE operation_id=?`, intent.ID, o.id); e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `UPDATE admin_operations SET state='running',error='',updated_at=? WHERE id=?`, w.Store.timestamp(), o.id)
			return e
		})
		if err != nil {
			state := "unknown"
			if errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrArtifactUnavailable) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalid) {
				state = "failed"
			}
			return true, errors.Join(err, outcome(state, err.Error()))
		}
		return true, nil
	}
	if o.phase != "resuming" {
		return true, outcome("failed", "invalid runtime control phase")
	}
	// Every additional provisioning/resume dispatch rechecks the actor and
	// owner allocation. On withdrawal, retain the operation until its admitted
	// effects are stopped; never declare failure while a placement still runs.
	validation := w.Store.write(ctx, func(tx *sql.Tx) error {
		current, e := adminProjectContinuationFor(ctx, tx, p.ID)
		if e != nil {
			return e
		}
		if current.id != o.id {
			return ErrConflict
		}
		actor, e := readAccount(ctx, tx, "id", o.actor)
		if e != nil {
			return e
		}
		if actor.Role != "admin" || actor.Status != "active" {
			return ErrForbidden
		}
		r, e := resources(ctx, tx, p.ID)
		if e != nil {
			return e
		}
		return admitCompute(ctx, tx, p.OwnerID, r.Profile, r.Occupied)
	})
	if validation != nil && (errors.Is(validation, ErrForbidden) || errors.Is(validation, ErrUnauthorized) || errors.Is(validation, ErrQuotaExceeded)) {
		err = w.Store.write(ctx, func(tx *sql.Tx) error {
			current, e := adminProjectContinuationFor(ctx, tx, p.ID)
			if e != nil {
				return e
			}
			if current.id != o.id {
				return ErrConflict
			}
			if _, e = tx.ExecContext(ctx, `UPDATE projects SET execution_suspended=1,execution_hold_reason='admin_resume_denied' WHERE id=?`, p.ID); e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `UPDATE admin_operation_targets SET phase='failing' WHERE operation_id=?`, o.id)
			return e
		})
		return true, err
	}
	if validation != nil {
		return true, errors.Join(validation, outcome("unknown", validation.Error()))
	}
	err = w.Lifecycle.Reconcile(ctx, p.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return true, errors.Join(err, outcome("unknown", err.Error()))
	}
	return true, w.Store.write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM project_operations WHERE id=? AND project_id=?`, o.lifecycle, p.ID).Scan(&state); err != nil {
			return err
		}
		if state == "canceled" {
			return w.Store.adminProjectOutcome(ctx, tx, o, "failed", "runtime intent canceled")
		}
		if state != "succeeded" {
			return nil
		}
		project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id=?`, p.ID))
		if err != nil {
			return err
		}
		if project.State != "active" || project.ExecutionSuspended {
			return ErrPending
		}
		return w.Store.adminProjectOutcome(ctx, tx, o, "succeeded", "")
	})
}
