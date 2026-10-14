package product

import (
	"context"
	"database/sql"
	"encoding/json"

	"harness-platform/orchestrator/internal/sandbox"
)

func profileConfiguration(actual sandbox.Configuration, p Profile, r Resources) bool {
	mounts, err := r.Mounts()
	if err != nil {
		return false
	}
	return actual.TemplateID == p.TemplateID && actual.CPUMillis == p.CPUMillis && actual.MemoryBytes == p.MemoryBytes && actual.DiskCapacityBytes == p.DiskBytes && sandbox.SameConfiguration(actual, p.Reachability.Declaration(), mounts)
}

// prepareConfiguration reconciles the recorded target before any Resume. A timeout
// leaves the same intent pending; only a full matching binding can settle it.
func (l *Lifecycle) prepareConfiguration(ctx context.Context, o operation, r Resources) error {
	var request operationRequest
	if err := json.Unmarshal([]byte(o.Request), &request); err != nil {
		return err
	}
	if request.CancelConfiguration {
		return l.resumeWithoutOptionalUpdate(ctx, o, r, request, request.CancelReason)
	}
	target := r.Profile
	if request.NetworkPolicy != nil {
		policy, err := NewNetworkPolicy(Reachability{Direct: request.NetworkPolicy.Direct, Resolvers: request.NetworkPolicy.Resolvers})
		if err != nil {
			return err
		}
		if policy.Revision != request.NetworkPolicy.Revision {
			return ErrConflict
		}
		target = policy.Apply(r.Profile)
	} else if request.UpdateExecution == "" {
		return ErrInvalid
	}
	var err error
	if request.Volumes != nil {
		target.Volumes = request.Volumes
	}
	targetResources := configurationResources(r, target, request.UpdateExecution != "")
	if err = target.Validate(); err != nil {
		return err
	}
	observed, err := l.Backend.InspectConfiguration(ctx, request.Reference)
	if err != nil {
		return err
	}
	ref := request.Reference
	validPlacement := func(c sandbox.Configuration) bool {
		return c.StoreID == ref.StoreID && c.SandboxID == ref.SandboxID && c.Epoch == ref.Epoch && c.State == "paused" && c.PlacementState == "lost" && c.MutationRevision != ""
	}
	if !validPlacement(observed) {
		return ErrConflict
	}
	sourceMatches := profileConfiguration(observed, r.Profile, r)
	targetMatches := profileConfiguration(observed, target, targetResources)
	if observed.Reference != ref {
		// Accept an uncertain successful PATCH only when it changed the complete
		// source declaration into exactly the recorded target on this placement.
		if !targetMatches || sourceMatches {
			return ErrConflict
		}
	} else if !targetMatches {
		if !sourceMatches {
			return ErrConflict
		}
		var owner string
		if err := l.Store.reader.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, o.ProjectID).Scan(&owner); err != nil {
			return err
		}
		if _, err := authorizeProjectConfiguration(ctx, l.Store.reader, owner, o.ProjectID); err != nil {
			return err
		}
		if request.UpdateExecution != "" {
			if err := l.checkPublicationTarget(ctx, o.ProjectID, request, r); err != nil {
				if o.State != "pending" {
					return l.resumeWithoutOptionalUpdate(ctx, o, r, request, err.Error())
				}
				return l.deferPublicationConfiguration(ctx, o, r, request, err.Error())
			}
		}
		mounts, e := targetResources.Mounts()
		if e != nil {
			return e
		}
		observed, err = l.Backend.UpdateConfiguration(ctx, ref, target.Reachability.Declaration(), mounts)
		if err != nil {
			if request.UpdateExecution != "" && (sandbox.Is(err, sandbox.Invalid) || sandbox.Is(err, sandbox.NotFound) || sandbox.Is(err, sandbox.Capacity)) {
				before, inspectErr := l.Backend.InspectConfiguration(ctx, ref)
				if inspectErr == nil && before.Reference == ref && validPlacement(before) && profileConfiguration(before, r.Profile, r) {
					if o.State != "pending" {
						return l.resumeWithoutOptionalUpdate(ctx, o, r, request, err.Error())
					}
					return l.deferPublicationConfiguration(ctx, o, r, request, err.Error())
				}
			}
			return err
		}
		if !validPlacement(observed) || !profileConfiguration(observed, target, targetResources) {
			return ErrConflict
		}
	}
	request.Reference = observed.Reference
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return err
	}
	rawProfile, err := json.Marshal(target)
	if err != nil {
		return err
	}
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET profile=?,mutation_revision=? WHERE project_id=?`, string(rawProfile), observed.MutationRevision, o.ProjectID); err != nil {
			return err
		}
		if request.UpdateExecution != "" {
			for _, slot := range targetResources.Volumes {
				if _, err := tx.ExecContext(ctx, `UPDATE project_volume_slots SET volume_id=? WHERE project_id=? AND slot_key=? AND storage='shared_readonly'`, slot.VolumeID, o.ProjectID, slot.Key); err != nil {
					return err
				}
			}
			if err := l.Store.commitEnvironmentUpdate(ctx, tx, o.ProjectID, request.UpdateExecution); err != nil {
				return err
			}
		}
		return changed(tx.ExecContext(ctx, `UPDATE project_operations SET phase='sandbox',request=?,state='pending',error='' WHERE id=? AND phase='configuration' AND state<>'succeeded'`, string(rawRequest), o.ID))
	})
}

// A definite no-effect result can discard only the optional data component.
// Mandatory network/access changes are still reconciled on the same activation.
func (l *Lifecycle) deferPublicationConfiguration(ctx context.Context, o operation, r Resources, request operationRequest, reason string) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		var release string
		if err := tx.QueryRowContext(ctx, `SELECT release_id FROM environment_update_executions WHERE id=?`, request.UpdateExecution).Scan(&release); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE environment_update_executions SET state='deferred',reason=?,updated_at=? WHERE id=?`, reason, l.Store.timestamp(), request.UpdateExecution); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_environment_updates SET state=CASE WHEN policy='fixed' THEN 'fixed' WHEN attempts>=3 THEN 'failed' ELSE 'deferred' END,reason=?,updated_at=? WHERE project_id=? AND target_id=?`, reason, l.Store.timestamp(), o.ProjectID, release); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, o.ProjectID).Scan(&owner); err != nil {
			return err
		}
		if _, err := authorizeProjectConfiguration(ctx, tx, owner, o.ProjectID); err != nil {
			return err
		}
		// Preserve the captured network target but refresh the current permission ceiling.
		plans, err := authorizedVolumePlan(ctx, tx, owner, o.ProjectID, r.Profile.Volumes)
		if err != nil {
			return err
		}
		request.Volumes = plans
		request.UpdateExecution = ""
		phase := "configuration"
		if request.NetworkPolicy == nil {
			phase = "sandbox"
		}
		if o.Kind == "create" {
			phase = "volumes"
		}
		raw, err := json.Marshal(request)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE project_operations SET phase=?,request=?,state='pending',error='' WHERE id=?`, phase, string(raw), o.ID)
		return err
	})
}

func (l *Lifecycle) checkPublicationTarget(ctx context.Context, project string, request operationRequest, r Resources) error {
	var release, state, policy, target string
	var stopped bool
	if err := l.Store.reader.QueryRowContext(ctx, `SELECT e.release_id,u.state,p.policy,p.target_id,u.stop_requested FROM environment_update_executions e JOIN environment_updates u ON u.id=e.release_id JOIN project_environment_updates p ON p.project_id=e.project_id WHERE e.id=?`, request.UpdateExecution).Scan(&release, &state, &policy, &target, &stopped); err != nil {
		return err
	}
	if stopped || (state != "active" && state != "superseded") || policy == "fixed" {
		return Admission("update_stopped", ErrConflict)
	}
	// A newer publication cannot rewrite this already captured target.
	for _, plan := range request.Volumes {
		if !updatablePublication(plan) {
			continue
		}
		if _, err := registeredBuiltin(ctx, l.Store.reader, plan.Source.Reference, true); err != nil {
			return err
		}
		if inspector, ok := l.Backend.(interface {
			InspectVolume(context.Context, sandbox.VolumeReference) (sandbox.Volume, error)
		}); ok {
			v, err := inspector.InspectVolume(ctx, sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: plan.Source.VolumeID})
			if err != nil {
				return err
			}
			if v.VolumeID != plan.Source.VolumeID || v.StoreID != r.Reference.StoreID || v.Capacity < plan.CapacityBytes {
				return ErrConflict
			}
		}
	}
	return nil
}

func configurationResources(r Resources, target Profile, replaceContent bool) Resources {
	targetResources := r
	targetResources.Profile = target
	targetResources.Volumes = append([]VolumeSlot(nil), r.Volumes...)
	if replaceContent {
		for i, slot := range targetResources.Volumes {
			for _, plan := range target.Volumes {
				if plan.Key == slot.Key && updatablePublication(plan) {
					targetResources.Volumes[i].VolumeID = plan.Source.VolumeID
				}
			}
		}
	}
	return targetResources
}

// Initial selection uses the same captured target and availability checks but
// needs no engine patch: the Project has no sandbox yet. The original slots and
// source references survive until this transaction commits the new selection.
func (l *Lifecycle) prepareInitialConfiguration(ctx context.Context, o operation, r Resources) error {
	var request operationRequest
	if err := json.Unmarshal([]byte(o.Request), &request); err != nil {
		return err
	}
	if request.UpdateExecution == "" || r.Reference.SandboxID != "" {
		return ErrConflict
	}
	var owner string
	if err := l.Store.reader.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, o.ProjectID).Scan(&owner); err != nil {
		return err
	}
	if _, err := authorizeProjectConfiguration(ctx, l.Store.reader, owner, o.ProjectID); err != nil {
		return err
	}
	if err := l.checkPublicationTarget(ctx, o.ProjectID, request, r); err != nil {
		return l.deferPublicationConfiguration(ctx, o, r, request, err.Error())
	}
	target := r.Profile
	target.Volumes = request.Volumes
	if err := validateRetainedProfile(ctx, l.Store.reader, target); err != nil {
		return err
	}
	raw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		for _, plan := range target.Volumes {
			if updatablePublication(plan) {
				if _, err := tx.ExecContext(ctx, `UPDATE project_volume_slots SET volume_id=? WHERE project_id=? AND slot_key=?`, plan.Source.VolumeID, o.ProjectID, plan.Key); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET profile=? WHERE project_id=?`, string(raw), o.ProjectID); err != nil {
			return err
		}
		if err := l.Store.commitEnvironmentUpdate(ctx, tx, o.ProjectID, request.UpdateExecution); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE project_operations SET phase='volumes',state='pending',error='' WHERE id=?`, o.ID)
		return err
	})
}

// Cancellation must fence a possibly delayed conditional PATCH before releasing
// its references. A source-shaped read alone does not prove a lost request has
// finished. A recorded no-op PATCH advances the validator, fencing that request.
func (l *Lifecycle) settleConfigurationCancellation(ctx context.Context, o operation, r Resources, continuation ...func(*sql.Tx) error) error {
	var request operationRequest
	if err := json.Unmarshal([]byte(o.Request), &request); err != nil {
		return err
	}
	actual, err := l.Backend.InspectConfiguration(ctx, request.Reference)
	absent := sandbox.Is(err, sandbox.NotFound)
	if err != nil && !absent {
		return err
	}
	if !absent {
		if actual.StoreID != request.Reference.StoreID || actual.SandboxID != request.Reference.SandboxID || actual.State != "paused" || actual.PlacementState != "lost" || actual.Epoch != request.Reference.Epoch {
			return ErrConflict
		}
		sourceMatches := profileConfiguration(actual, r.Profile, r)
		if sourceMatches && actual.Reference == request.Reference && (o.State != "pending" || request.CancelConfiguration) {
			request.CancelConfiguration = true
			raw, e := json.Marshal(request)
			if e != nil {
				return e
			}
			if e = l.Store.write(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `UPDATE project_operations SET request=?,state='unknown' WHERE id=? AND state NOT IN ('succeeded','canceled')`, string(raw), o.ID)
				return e
			}); e != nil {
				return e
			}
			mounts, e := r.Mounts()
			if e != nil {
				return e
			}
			actual, e = l.Backend.UpdateConfiguration(ctx, request.Reference, r.Profile.Reachability.Declaration(), mounts)
			if e != nil {
				return e
			}
			if actual.Reference == request.Reference || actual.State != "paused" || actual.PlacementState != "lost" || actual.Epoch != request.Reference.Epoch || !profileConfiguration(actual, r.Profile, r) {
				return ErrConflict
			}
		} else if !sourceMatches || (actual.Reference != request.Reference && !request.CancelConfiguration) {
			target := r.Profile
			if request.NetworkPolicy != nil {
				target = request.NetworkPolicy.Apply(target)
			}
			if request.Volumes != nil {
				target.Volumes = request.Volumes
			}
			targetResources := configurationResources(r, target, request.UpdateExecution != "")
			if !profileConfiguration(actual, target, targetResources) {
				return ErrConflict
			}
			settled := request
			settled.CancelConfiguration = false
			raw, e := json.Marshal(settled)
			if e != nil {
				return e
			}
			observedOperation := o
			observedOperation.Request = string(raw)
			if err := l.prepareConfiguration(ctx, observedOperation, r); err != nil {
				return err
			}
			// The exact admitted effect is now committed; cancellation never resumes it.
			latest, e := l.Store.resourcesFor(ctx, o.ProjectID)
			if e != nil {
				return e
			}
			actual.Reference = latest.Reference
		}
	}
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		if !absent {
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET mutation_revision=? WHERE project_id=?`, actual.MutationRevision, o.ProjectID); err != nil {
				return err
			}
		}
		if err := l.Store.cancelEnvironmentExecution(ctx, tx, o.ProjectID, "Project stopped before update completion"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_operations SET state='canceled',error='' WHERE id=?`, o.ID); err != nil {
			return err
		}
		if len(continuation) > 0 {
			return continuation[0](tx)
		}
		return nil
	})
}

// If a previous PATCH may still be in flight, fallback first fences or adopts it.
// The replacement resume is admitted in the cancellation transaction so a crash
// cannot strand a resuming Project without an operation. It cannot attempt a
// second optional update during this activation.
func (l *Lifecycle) resumeWithoutOptionalUpdate(ctx context.Context, o operation, r Resources, request operationRequest, reason string) error {
	request.CancelReason = reason
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	o.Request = string(raw)
	return l.settleConfigurationCancellation(ctx, o, r, func(tx *sql.Tx) error {
		var state, release string
		if err := tx.QueryRowContext(ctx, `SELECT state,release_id FROM environment_update_executions WHERE id=?`, request.UpdateExecution).Scan(&state, &release); err != nil {
			return err
		}
		if state != "succeeded" {
			if _, err := tx.ExecContext(ctx, `UPDATE environment_update_executions SET state='deferred',reason=?,updated_at=? WHERE id=?`, reason, l.Store.timestamp(), request.UpdateExecution); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_environment_updates SET state=CASE WHEN policy='fixed' THEN 'fixed' WHEN attempts>=3 THEN 'failed' ELSE 'deferred' END,reason=?,updated_at=? WHERE project_id=? AND target_id=?`, reason, l.Store.timestamp(), o.ProjectID, release); err != nil {
				return err
			}
		}
		latest, err := resources(ctx, tx, o.ProjectID)
		if err != nil {
			return err
		}
		return l.newOperation(ctx, tx, o.ProjectID, "resume", "sandbox", operationRequest{Reference: latest.Reference, SkipEnvironmentUpdate: true})
	})
}

// Both supported content update kinds use immutable read-only bindings. System
// content additionally requires its dedicated profile; config is never mutable.
func updatablePublication(plan VolumePlan) bool {
	if plan.Source == nil || plan.Storage != "shared_readonly" || plan.Access != "read_only" {
		return false
	}
	return plan.Role == "data" && (plan.Source.ContentProfile == "authored-v1" || plan.Source.ContentProfile == "reviewed-v1") || plan.Role == "shared_assets" && plan.Source.ContentProfile == "shared-assets-v1"
}
