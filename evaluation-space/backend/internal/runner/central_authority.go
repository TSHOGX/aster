package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

type GenerationAuthority interface {
	CancelGenerationAdmission(context.Context, evolution.GenerationAdmissionCancellation) (evolution.GenerationGrant, error)
	AdmitGeneration(context.Context, evolution.GenerationAssignment) (evolution.GenerationGrant, error)
	ResolveGeneration(context.Context, evolution.GenerationGrantScope) (evolution.GenerationAssignment, error)
	ObserveGeneration(context.Context, evolution.GenerationObservation) error
	SettleGeneration(context.Context, evolution.GenerationGrant, int64, int64, int64) error
}
type CentralAuthority struct {
	Store   *projections.Store
	Central GenerationAuthority
}

func (a CentralAuthority) load(ctx context.Context, run evolution.Run) (evolution.GenerationGrant, error) {
	if a.Store == nil || a.Central == nil {
		return evolution.GenerationGrant{}, fmt.Errorf("central native authority required")
	}
	assignment, grant, err := a.Store.GenerationGrant(ctx, run.GroupID, run.ID)
	pending := errors.Is(err, projections.ErrNotFound)
	if pending {
		assignment, err = a.Store.PendingGenerationAssignment(ctx, run.GroupID, run.ID)
	}
	if err != nil {
		return grant, err
	}
	actual, _ := json.Marshal(run.Pins)
	expected, _ := json.Marshal(assignment.Pins)
	if run.BatchID != assignment.BatchID || !bytes.Equal(actual, expected) || run.BindingID != assignment.BindingID || run.Budget != assignment.Budget {
		return grant, projections.ErrConflict
	}
	if pending {
		if run.State == "cancelled" {
			grant, err = a.Central.CancelGenerationAdmission(ctx, evolution.GenerationAdmissionCancellation{Assignment: assignment, Fence: run.Fence})
		} else {
			grant, err = a.Central.AdmitGeneration(ctx, assignment)
		}
		if err != nil {
			return grant, err
		}
		if err = a.Store.RetainGenerationGrant(ctx, assignment, grant); err != nil {
			return grant, err
		}
	}
	return grant, nil
}
func (a CentralAuthority) AuthorizeRun(ctx context.Context, run evolution.Run, cleanup bool) error {
	grant, err := a.load(ctx, run)
	if err != nil {
		return err
	}
	// On queued/resumed/cancellation work, publish the local durable control fence
	// before checking authority. Claim hooks publish the later worker lease fence.
	state := run.State
	if state == "queued" {
		if run.Fence > 0 {
			state = "paused"
		} else {
			state = "admitted"
		}
	}
	if state != "admitted" {
		if err = a.Central.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: run.Fence, State: state}); err != nil {
			return err
		}
	}
	action := "execute"
	if run.State == "queued" {
		action = "read"
	}
	if cleanup {
		action = "cleanup"
	}
	if run.State == "completed" {
		action = "handoff"
	}
	_, err = a.Central.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Action: action, Fence: run.Fence})
	if err != nil {
		return err
	}
	if !cleanup {
		service, err := a.Store.ServiceState(ctx, run.Pins.Service.ID)
		if err != nil {
			return err
		}
		if !service.Enabled {
			return fmt.Errorf("generation service revoked")
		}
	}
	return nil
}
func (a CentralAuthority) AuthorizeClaim(ctx context.Context, lease projections.RunLease, run evolution.Run, cleanup bool) error {
	run.Fence = lease.Fence
	run.State = "running"
	if cleanup {
		run.State = "cancelling"
	}
	return a.AuthorizeRun(ctx, run, cleanup)
}
func (a CentralAuthority) AuthorizeArtifact(ctx context.Context, run evolution.Run, artifact evolution.ArtifactRef) error {
	if artifact.Validate() != nil || artifact.GroupID != run.GroupID {
		return projections.ErrConflict
	}
	grant, err := a.load(ctx, run)
	if err != nil {
		return err
	}
	if err = a.AuthorizeRun(ctx, run, false); err != nil {
		return err
	}
	for _, input := range run.Pins.Inputs {
		if input == artifact {
			return nil
		}
	}
	if artifact.GrantID != grant.ID || artifact.PolicyRevision != run.Pins.SourcePolicyRevision || artifact.Size > run.Budget.MaxArtifactBytes {
		return projections.ErrConflict
	}
	return nil
}
func (a CentralAuthority) OutputGrant(ctx context.Context, run evolution.Run) (string, error) {
	grant, err := a.load(ctx, run)
	if err != nil {
		return "", err
	}
	if err = a.AuthorizeRun(ctx, run, false); err != nil {
		return "", err
	}
	return grant.ID, nil
}
func (a CentralAuthority) ReconcileTerminal(ctx context.Context, run evolution.Run) error {
	if run.State != "completed" && run.State != "failed" && run.State != "cancelled" {
		return projections.ErrConflict
	}
	grant, err := a.load(ctx, run)
	if err != nil {
		return err
	}
	if err = a.Central.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: run.Fence, State: run.State}); err != nil {
		return err
	}
	budget, err := a.Store.RunBudget(ctx, run.GroupID, run.ID)
	if err != nil {
		return err
	}
	if budget.UnsettledCalls != 0 {
		return projections.ErrConflict
	}
	if err = a.Central.SettleGeneration(ctx, grant, run.Fence, budget.Spent.Tokens, budget.Spent.CostMicros); err != nil {
		return err
	}
	return a.Store.AcknowledgeGenerationSettlement(ctx, run.GroupID, run.ID)
}
