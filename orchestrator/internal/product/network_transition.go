package product

import (
	"context"
	"database/sql"
	"encoding/json"

	"harness-platform/orchestrator/internal/sandbox"
)

func profileConfiguration(actual sandbox.Configuration, p Profile, r Resources) bool {
	mounts := append([]sandbox.Mount{}, p.PublishedMounts...)
	mounts = append(mounts, sandbox.Mount{VolumeID: r.WorkspaceVolume, Destination: "/workspace", Access: "exclusive"}, sandbox.Mount{VolumeID: r.RuntimeVolume, Destination: "/runtime", Access: "exclusive"})
	return actual.TemplateID == p.TemplateID && actual.CPUMillis == p.CPUMillis && actual.MemoryBytes == p.MemoryBytes && actual.DiskCapacityBytes == p.DiskBytes && sandbox.SameConfiguration(actual, p.Reachability.Declaration(), mounts)
}

// prepareNetwork reconciles the recorded target before any Resume. A timeout
// leaves the same intent pending; only a full matching binding can settle it.
func (l *Lifecycle) prepareNetwork(ctx context.Context, o operation, r Resources) error {
	var request operationRequest
	if err := json.Unmarshal([]byte(o.Request), &request); err != nil {
		return err
	}
	if request.NetworkPolicy == nil {
		return ErrInvalid
	}
	policy, err := NewNetworkPolicy(Reachability{Direct: request.NetworkPolicy.Direct, Resolvers: request.NetworkPolicy.Resolvers})
	if err != nil {
		return err
	}
	if policy.Revision != request.NetworkPolicy.Revision {
		return ErrConflict
	}
	target := policy.Apply(r.Profile)
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
	targetMatches := profileConfiguration(observed, target, r)
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
		observed, err = l.Backend.UpdateNetwork(ctx, ref, target.Reachability.Declaration())
		if err != nil {
			return err
		}
		if !validPlacement(observed) || !profileConfiguration(observed, target, r) {
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
		return changed(tx.ExecContext(ctx, `UPDATE project_operations SET phase='sandbox',request=?,state='pending',error='' WHERE id=? AND phase='network' AND state<>'succeeded'`, string(rawRequest), o.ID))
	})
}
