package khora

import (
	"context"
	"fmt"
	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/sandbox"
)

// UpdateConfiguration changes a paused placement's network and mount access in
// one conditional engine mutation, so recovery can compare the whole target.
func (b *Backend) UpdateConfiguration(ctx context.Context, ref sandbox.Reference, network sandbox.Network, mounts []sandbox.Mount) (sandbox.Configuration, error) {
	if err := b.validateReference(ctx, ref, true); err != nil {
		return sandbox.Configuration{}, err
	}
	declaration, err := networkDeclaration(network)
	if err != nil {
		return sandbox.Configuration{}, err
	}
	volumes, err := volumeBindings(mounts)
	if err != nil {
		return sandbox.Configuration{}, err
	}
	if _, err = b.client.PatchIfMatch(ctx, ref.SandboxID, ref.MutationRevision, sdk.PatchRequest{Network: &declaration, Volumes: &volumes}); err != nil {
		return sandbox.Configuration{}, mapError("update configuration", err)
	}
	observed, err := b.InspectConfiguration(ctx, ref)
	if err != nil {
		return observed, err
	}
	if observed.State != "paused" || observed.PlacementState != "lost" || observed.Epoch != ref.Epoch {
		return observed, failure("update configuration", sandbox.Uncertain, fmt.Errorf("configuration replacement did not retain the paused placement"))
	}
	return observed, nil
}
