package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

func executionAllowed(ctx context.Context, q queryer, project string) error {
	var held bool
	if err := missing(q.QueryRowContext(ctx, `SELECT execution_suspended FROM projects WHERE id=?`, project).Scan(&held)); err != nil {
		return err
	}
	if held {
		return Admission("project_suspended", ErrForbidden)
	}
	return nil
}
func authorizeExecution(ctx context.Context, q queryer, scope Scope, action ProductAction) error {
	if err := authorizeAction(ctx, q, scope.Actor, action); err != nil {
		return err
	}
	if err := noAdminProjectControl(ctx, q, scope.ProjectID); err != nil {
		return err
	}
	return executionAllowed(ctx, q, scope.ProjectID)
}

// stopHeldPlacement runs under the same Project lane as ordinary reconciliation.
// It settles only admitted compute effects and never materializes another slot,
// projects guest configuration or admits child work. Persistent storage remains.
func (l *Lifecycle) stopHeldPlacement(ctx context.Context, project string) error {
	r, err := l.Store.resourcesFor(ctx, project)
	if errors.Is(err, ErrNotFound) {
		return l.finishExecutionHold(ctx, project, nil)
	}
	if err != nil {
		return err
	}
	o, err := pendingOperation(ctx, l.Store.reader, project)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if r.Reference.SandboxID == "" {
		if o.Kind == "create" && o.Phase == "sandbox" && o.State != "pending" {
			// A dispatched create whose reply was lost keeps its original key and
			// complete request. Settle that admitted effect, then immediately pause it;
			// do not proceed to guest projection, initialization or user execution.
			mounts, e := r.Mounts()
			if e != nil {
				return e
			}
			p := r.Profile
			obs, e := l.Backend.Create(ctx, sandbox.CreateRequest{IdempotencyKey: o.ID + "/sandbox", TemplateID: p.TemplateID, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, DiskBytes: p.DiskBytes, Network: p.Reachability.Declaration(), Mounts: mounts})
			if e != nil {
				return e
			}
			if obs.StoreID != r.Reference.StoreID || obs.SandboxID == "" || obs.Epoch == 0 || obs.MutationRevision == "" {
				return ErrConflict
			}
			if e = l.Store.write(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `UPDATE project_resources SET sandbox_id=?,epoch=?,mutation_revision=? WHERE project_id=?`, obs.SandboxID, obs.Epoch, obs.MutationRevision, project)
				return e
			}); e != nil {
				return e
			}
			r.Reference = obs.Reference
		} else {
			return l.finishExecutionHold(ctx, project, nil)
		}
	}
	observed, err := l.Backend.Inspect(ctx, r.Reference)
	if sandbox.Is(err, sandbox.NotFound) {
		return l.finishExecutionHold(ctx, project, nil)
	}
	if err != nil {
		return err
	}
	if observed.StoreID != r.Reference.StoreID || observed.SandboxID != r.Reference.SandboxID || observed.Epoch < r.Reference.Epoch {
		return ErrConflict
	}
	if observed.State == "running" && observed.PlacementState == "active" {
		// A lost resume reply may have advanced the epoch. Pause the observed exact
		// incarnation; suspension never adopts it for further work.
		observed, err = l.Backend.Pause(ctx, observed.Reference)
		if err != nil {
			return err
		}
	}
	if observed.State != "paused" || observed.PlacementState != "lost" || observed.MutationRevision == "" {
		return ErrPending
	}
	return l.finishExecutionHold(ctx, project, &observed)
}
func (l *Lifecycle) finishExecutionHold(ctx context.Context, project string, observation *sandbox.Observation) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		var held bool
		var state, reason string
		if err := tx.QueryRowContext(ctx, `SELECT execution_suspended,state,execution_hold_reason FROM projects WHERE id=?`, project).Scan(&held, &state, &reason); err != nil {
			return err
		}
		if !held || state == "deleting" || state == "deleted" {
			return ErrConflict
		}
		if observation != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET epoch=?,mutation_revision=?,occupied=0 WHERE project_id=?`, observation.Epoch, observation.MutationRevision, project); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET occupied=0,sandbox_id='',epoch=0,mutation_revision='' WHERE project_id=?`, project); err != nil {
				return err
			}
		}
		if err := l.Store.loseStaleAgents(ctx, tx, project, 0, reason+"; placement stopped; native outcome may require recovery"); err != nil {
			return err
		}
		if _, err := l.Store.loseStalePTYs(ctx, tx, project, 0, reason+"; placement stopped"); err != nil {
			return err
		}
		if err := l.Store.settleLostActivities(ctx, tx, project); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_operations SET state='canceled',error=? WHERE project_id=? AND kind IN ('create','resume','pause') AND state NOT IN ('succeeded','canceled')`, reason, project); err != nil {
			return err
		}
		// Unbound metadata keeps its created state. Bound plans preserve all slot
		// identities and charges, including unfinished initialization.
		result, err := tx.ExecContext(ctx, `UPDATE projects SET state=CASE WHEN EXISTS(SELECT 1 FROM project_resources WHERE project_id=projects.id) THEN 'paused' ELSE 'created' END,failure='',idle_deadline=NULL WHERE id=? AND (state NOT IN ('paused','created') OR failure<>'' OR idle_deadline IS NOT NULL)`, project)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			return l.Store.bumpProject(ctx, tx, project, "project.suspended")
		}
		return nil
	})
}
func (w *Workers) suspendProject(ctx context.Context, p Project) error {
	if err := w.stopProjectChildren(ctx, p.ID); err != nil {
		return err
	}
	err := w.Lifecycle.Reconcile(ctx, p.ID)
	state := "succeeded"
	detail := ""
	if err != nil {
		state = "unknown"
		detail = fmt.Sprintf("placement stop not confirmed: %v", err)
	}
	if errors.Is(err, ErrPending) {
		state = "running"
	}
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	recordCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer done()
	recorded := w.Store.write(recordCtx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(recordCtx, `UPDATE admin_operation_targets SET state=?,error=? WHERE project_id=? AND state<>'succeeded' AND operation_id IN (SELECT id FROM admin_operations WHERE action='account.suspend' AND state<>'succeeded')`, state, detail, p.ID)
		return e
	})
	return errors.Join(err, recorded)
}
func (w *Workers) stopProjectChildren(ctx context.Context, project string) error {
	// Bounded graceful requests reduce interrupted native work. The placement
	// pause provides mandatory isolation-unit stop evidence, even without a local
	// attachment or with an unresponsive child. Never delete conversation data.
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT session_id,'agent' FROM agent_incarnations WHERE project_id=? AND state IN ('starting','running','stopping') UNION ALL SELECT terminal_id,'terminal' FROM terminal_incarnations WHERE project_id=? AND state IN ('starting','running') LIMIT 32`, project, project)
	if err != nil {
		return err
	}
	type child struct{ id, kind string }
	children := []child{}
	for rows.Next() {
		var c child
		if err = rows.Scan(&c.id, &c.kind); err != nil {
			break
		}
		children = append(children, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	stopCtx, cancelStops := context.WithTimeout(ctx, 2*time.Second)
	for _, c := range children {
		if stopCtx.Err() != nil {
			break
		}
		if c.kind == "agent" && w.Agents != nil {
			w.report("suspension agent stop", w.Agents.Stop(stopCtx, project, c.id))
		}
		if c.kind == "terminal" && w.Terminals != nil {
			w.report("suspension terminal stop", w.Terminals.Stop(stopCtx, project, c.id))
		}
	}
	cancelStops()
	return nil
}
func (w *Workers) suspensionOperations(ctx context.Context) error {
	var pending bool
	if err := w.Store.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_operations WHERE action='account.suspend' AND state<>'succeeded')`).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	return w.Store.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,actor_id,target_id FROM admin_operations WHERE action='account.suspend' AND state<>'succeeded' ORDER BY updated_at,id LIMIT 16`)
		if err != nil {
			return err
		}
		type op struct{ id, actor, target string }
		operations := []op{}
		for rows.Next() {
			var value op
			if err = rows.Scan(&value.id, &value.actor, &value.target); err != nil {
				break
			}
			operations = append(operations, value)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, o := range operations {
			// Deletion remains an independent authorized reduction and establishes the
			// absence required by a suspension target as well.
			if _, err = tx.ExecContext(ctx, `UPDATE admin_operation_targets SET state='succeeded',error='' WHERE operation_id=? AND project_id IN (SELECT id FROM projects WHERE state='deleted')`, o.id); err != nil {
				return err
			}
			var pending, unknown int
			if err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(state<>'succeeded'),0),coalesce(sum(state='unknown'),0) FROM admin_operation_targets WHERE operation_id=?`, o.id).Scan(&pending, &unknown); err != nil {
				return err
			}
			state := "running"
			if pending == 0 {
				state = "succeeded"
			} else if unknown > 0 {
				state = "unknown"
			}
			if _, err = tx.ExecContext(ctx, `UPDATE admin_operations SET state=?,updated_at=? WHERE id=?`, state, w.Store.timestamp(), o.id); err != nil {
				return err
			}
			if pending == 0 {
				if err = w.Store.auditEvent(ctx, tx, o.actor, "account.suspend.completed", "account", o.target, "", 0, map[string]any{"operation_id": o.id, "state": "succeeded"}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
