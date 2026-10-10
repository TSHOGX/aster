package product

import (
	"context"
	"database/sql"
	"errors"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

// EnsureConfiguredActive retains fixed Project configuration and selects mutable
// network defaults in the transaction recording creation/resume intent.
func (l *Lifecycle) EnsureConfiguredActive(ctx context.Context, scope Scope, fallback Profile) error {
	return l.ensureActive(ctx, scope, fallback, true, false)
}

// Observe validates the original binding, including after engine events are lost.
// It never resolves uncertainty by silently retargeting another engine store.
func (l *Lifecycle) Observe(ctx context.Context, project string) error {
	lane := l.lane(project)
	lane.Lock()
	defer lane.Unlock()
	var r Resources
	var priorState string
	err := l.Store.write(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM projects WHERE id=?`, project).Scan(&state); err != nil {
			return missing(err)
		}
		priorState = state
		if state != "active" && state != "paused" && state != "blocked" {
			return ErrPending
		}
		if _, err := pendingOperation(ctx, tx, project); err == nil {
			return ErrPending
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		var err error
		r, err = resources(ctx, tx, project)
		if err != nil {
			return err
		}
		if r.Reference.SandboxID == "" {
			return ErrPending
		}
		result, err := tx.ExecContext(ctx, `UPDATE project_resources SET observed_at=? WHERE project_id=? AND observed_at<=?`, l.Store.timestamp(), project, sqlitex.FormatTime(l.Store.now().Add(-5*time.Second)))
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return ErrPending
		}
		return nil
	})
	if err != nil {
		return err
	}
	identity, identityErr := l.Backend.Identity(ctx)
	if sandbox.Is(identityErr, sandbox.IdentityChanged) || (identityErr == nil && identity.StoreID != r.Reference.StoreID) {
		return l.fenceObservation(ctx, project, "engine store identity changed; original resources remain accounted")
	}
	if identityErr != nil {
		return identityErr
	}
	observation, err := l.Backend.Inspect(ctx, r.Reference)
	if err != nil {
		if sandbox.Is(err, sandbox.IdentityChanged) {
			return l.fenceObservation(ctx, project, "engine store identity changed; original resources remain accounted")
		}
		if !sandbox.Is(err, sandbox.NotFound) {
			return err
		}
		// The original store confirmed sandbox absence. Recreate only its placement,
		// retaining the Project-owned volumes and preserving the lost binding in intent.
		return l.Store.write(ctx, func(tx *sql.Tx) error {
			if err := l.Store.loseStaleAgents(ctx, tx, project, 0, "sandbox absent; native outcome requires reconciliation"); err != nil {
				return err
			}
			if _, err := l.Store.loseStalePTYs(ctx, tx, project, 0, "sandbox absent"); err != nil {
				return err
			}
			if err := l.Store.settleLostActivities(ctx, tx, project); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET occupied=0 WHERE project_id=?`, project); err != nil {
				return err
			}
			// The old placement is confirmed absent. A replacement reserves capacity
			// in its reconciliation transaction before any remote create effect.
			return l.newOperation(ctx, tx, project, "create", "sandbox", operationRequest{Reference: r.Reference})
		})
	}
	if observation.StoreID != r.Reference.StoreID || observation.SandboxID != r.Reference.SandboxID || observation.Epoch < r.Reference.Epoch {
		return l.fenceObservation(ctx, project, "contradictory engine placement identity")
	}
	if observation.State == "running" && observation.PlacementState == "active" {
		if observation.Epoch == r.Reference.Epoch && observation.MutationRevision == r.Reference.MutationRevision {
			if priorState == "blocked" {
				return l.Store.write(ctx, func(tx *sql.Tx) error {
					if _, err := tx.ExecContext(ctx, `UPDATE projects SET state='active',failure='' WHERE id=?`, project); err != nil {
						return err
					}
					return l.Store.bumpProject(ctx, tx, project, "project.identity_verified")
				})
			}
			return nil
		}
		if observation.Epoch == r.Reference.Epoch {
			return l.fenceObservation(ctx, project, "Project configuration changed outside its lifecycle authority")
		}
		return l.Store.write(ctx, func(tx *sql.Tx) error {
			if err := l.Store.loseStaleAgents(ctx, tx, project, observation.Epoch, "placement replaced; reconcile original native history"); err != nil {
				return err
			}
			if _, err := l.Store.loseStalePTYs(ctx, tx, project, observation.Epoch, "placement replaced"); err != nil {
				return err
			}
			if err := l.Store.settleLostActivities(ctx, tx, project); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET mutation_revision=?,epoch=?,occupied=1 WHERE project_id=?`, observation.MutationRevision, observation.Epoch, project); err != nil {
				return err
			}
			return l.newOperation(ctx, tx, project, "resume", "reachability", operationRequest{Reference: observation.Reference})
		})
	}
	if observation.State == "paused" && observation.PlacementState == "lost" {
		if priorState == "paused" && observation.Reference == r.Reference {
			return nil
		}
		return l.Store.write(ctx, func(tx *sql.Tx) error {
			if err := l.Store.loseStaleAgents(ctx, tx, project, 0, "placement lost; reconcile original native history"); err != nil {
				return err
			}
			if _, err := l.Store.loseStalePTYs(ctx, tx, project, 0, "placement lost"); err != nil {
				return err
			}
			if err := l.Store.settleLostActivities(ctx, tx, project); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET mutation_revision=?,epoch=?,occupied=0 WHERE project_id=?`, observation.MutationRevision, observation.Epoch, project); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET state='paused',failure='',idle_deadline=NULL WHERE id=?`, project); err != nil {
				return err
			}
			return l.Store.bumpProject(ctx, tx, project, "project.placement_lost")
		})
	}
	return l.fenceObservation(ctx, project, "engine placement is not ready; original binding retained")
}
func (l *Lifecycle) fenceObservation(ctx context.Context, project, reason string) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE projects SET state='blocked',failure=?,idle_deadline=NULL WHERE id=? AND (state<>'blocked' OR failure<>?)`, reason, project, reason)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return nil
		}
		return l.Store.bumpProject(ctx, tx, project, "project.blocked")
	})
}
func (s *Store) settleLostActivities(ctx context.Context, tx *sql.Tx, project string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_incarnations SET cleanup_state='settled' WHERE project_id=? AND state='exited'`, project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_activity SET state='complete',updated_at=? WHERE project_id=? AND kind IN ('files','descendants','agent_start','terminal_start','terminal_control') AND id NOT IN (SELECT 'file:'||id FROM file_operations WHERE volume_id<>'')`, s.timestamp(), project); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE file_operations SET state=CASE WHEN state='running' THEN 'unknown' ELSE state END,settled=1,updated_at=? WHERE project_id=? AND settled=0 AND volume_id=''`, s.timestamp(), project)
	return err
}
func (l *Lifecycle) ForceRestart(ctx context.Context, scope Scope, revision int64) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		p, err := mutableOwnedProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if err := authorizeAction(ctx, tx, scope.Actor, ActionProjectsManage); err != nil {
			return err
		}
		if p.Revision != revision || p.State != "active" {
			return ErrConflict
		}
		if _, err = pendingOperation(ctx, tx, p.ID); err == nil {
			return ErrPending
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		r, err := resources(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE turns SET queue_policy='held',updated_at=? WHERE project_id=? AND disposition='pending' AND queue_policy='released'`, l.Store.timestamp(), p.ID); err != nil {
			return err
		}
		return l.newOperation(ctx, tx, p.ID, "pause", "sandbox", operationRequest{Reference: r.Reference, Restart: true, Force: true})
	})
}
