package khora

import (
	"context"
	"fmt"
	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/sandbox"
)

func (b *Backend) UpdateNetwork(ctx context.Context, ref sandbox.Reference, network sandbox.Network) (sandbox.Configuration, error) {
	if err := b.validateReference(ctx, ref, true); err != nil {
		return sandbox.Configuration{}, err
	}
	declaration, err := networkDeclaration(network)
	if err != nil {
		return sandbox.Configuration{}, err
	}
	if _, err = b.client.PatchIfMatch(ctx, ref.SandboxID, ref.MutationRevision, sdk.PatchRequest{Network: &declaration}); err != nil {
		return sandbox.Configuration{}, mapError("update network", err)
	}
	observed, err := b.InspectConfiguration(ctx, ref)
	if err != nil {
		return observed, err
	}
	if observed.State != "paused" || observed.PlacementState != "lost" || observed.Epoch != ref.Epoch {
		return observed, failure("update network", sandbox.Uncertain, fmt.Errorf("network replacement did not retain the paused placement"))
	}
	return observed, nil
}
