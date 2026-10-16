// Package handoff transfers committed generation results independently of model
// execution. Remote uncertainty is reconciled by the same immutable run/patch key.
package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/workflow"
)

var ErrNotFound = evolution.ErrRunResultNotFound

type Receipt = evolution.AcceptedRunResult
type Studio interface {
	Base(context.Context, evolution.Run) ([]manifest.Entry, error)
	Lookup(context.Context, evolution.Run, evolution.Patch) (Receipt, error)
	Submit(context.Context, evolution.Run, evolution.Patch) (Receipt, error)
}
type Authority interface {
	Check(context.Context, evolution.Run) error
	Artifact(context.Context, evolution.Run, evolution.ArtifactRef) error
	OutputGrant(context.Context, evolution.Run) (string, error)
}
type Artifact struct {
	Reference evolution.ArtifactRef
	Bytes     []byte
}
type Planner interface {
	Plan(evolution.Run, workflow.Values, []manifest.Entry, string) (evolution.Patch, []Artifact, error)
}
type Worker struct {
	Planner   Planner
	Store     *projections.Store
	Studio    Studio
	Authority Authority
}

func (w Worker) Step(ctx context.Context, group, id string) error {
	if w.Store == nil || w.Studio == nil || w.Authority == nil || w.Planner == nil {
		return fmt.Errorf("handoff dependencies required")
	}
	run, err := w.Store.Run(ctx, group, id)
	if err != nil {
		return err
	}
	if run.DraftTest != nil || run.State != "completed" {
		return projections.ErrConflict
	}
	if err = w.Authority.Check(ctx, run); err != nil {
		return err
	}
	retained, err := w.Store.Handoff(ctx, group, id)
	if errors.Is(err, projections.ErrNotFound) {
		checkpoint, err := w.Store.LatestRunCheckpoint(ctx, group, id)
		if err != nil {
			return err
		}
		if checkpoint.ID != "workflow-result" {
			return projections.ErrConflict
		}
		if err = w.Authority.Artifact(ctx, run, checkpoint.Artifact); err != nil {
			return err
		}
		raw, err := w.Store.NonDatasetArtifact(ctx, checkpoint.Artifact)
		if err != nil {
			return err
		}
		outputs, err := evolution.Decode[workflow.Values](bytes.NewReader(raw))
		if err != nil {
			return err
		}
		base, err := w.Studio.Base(ctx, run)
		if err != nil {
			return err
		}
		grant, err := w.Authority.OutputGrant(ctx, run)
		if err != nil {
			return err
		}
		patch, artifacts, err := w.Planner.Plan(run, outputs, base, grant)
		if err != nil {
			return err
		}
		for _, artifact := range artifacts {
			if err = w.Authority.Artifact(ctx, run, artifact.Reference); err != nil {
				return err
			}
			if err = w.Store.SealArtifact(ctx, artifact.Reference, bytes.NewReader(artifact.Bytes)); err != nil {
				return err
			}
		}
		if err = w.Authority.Check(ctx, run); err != nil {
			return err
		}
		retained, err = w.Store.PrepareHandoff(ctx, group, patch)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if retained.State == "submitted" || retained.State == "no_change" {
		return nil
	}
	receipt, err := w.Studio.Lookup(ctx, run, retained.Patch)
	if errors.Is(err, ErrNotFound) {
		if err = w.Authority.Check(ctx, run); err != nil {
			return err
		}
		receipt, err = w.Studio.Submit(ctx, run, retained.Patch)
	}
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(retained.Patch)
	if receipt.RunID != id || receipt.PatchDigest != evolution.Digest(raw) {
		return projections.ErrConflict
	}
	if err = w.Authority.Check(ctx, run); err != nil {
		return err
	}
	return w.Store.AcceptHandoff(ctx, group, retained.Patch, receipt.BranchID, receipt.CommitID)
}
