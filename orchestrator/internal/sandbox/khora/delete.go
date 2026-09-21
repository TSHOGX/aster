package khora

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
)

// Destroy settles only the recorded sandbox and conditional revision. A lost
// successful reply is resolved by absence, never by adopting a successor token.
func (b *Backend) Destroy(ctx context.Context, ref sandbox.Reference) error {
	if err := b.validateReference(ctx, ref, true); err != nil {
		return err
	}
	err := mapError("destroy", b.client.DestroyIfMatch(ctx, ref.SandboxID, ref.MutationRevision))
	if err != nil && !sandbox.Is(err, sandbox.NotFound) && !sandbox.Is(err, sandbox.Precondition) {
		return err
	}
	_, inspectErr := b.Inspect(ctx, ref)
	if sandbox.Is(inspectErr, sandbox.NotFound) {
		return nil
	}
	if inspectErr != nil {
		return inspectErr
	}
	if err != nil {
		return err
	}
	return failure("destroy", sandbox.Uncertain, fmt.Errorf("sandbox still exists after destroy"))
}
func (b *Backend) DeleteVolume(ctx context.Context, ref sandbox.VolumeReference) error {
	if ref.VolumeID == "" || ref.StoreID != b.identity.StoreID {
		return failure("delete volume", sandbox.Invalid, fmt.Errorf("original volume identity required"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return err
	}
	err := mapError("delete volume", b.client.DeleteVolume(ctx, ref.VolumeID))
	if err != nil && !sandbox.Is(err, sandbox.NotFound) {
		return err
	}
	_, inspectErr := b.client.Volume(ctx, ref.VolumeID)
	if _, identityErr := b.Identity(ctx); identityErr != nil {
		return identityErr
	}
	inspectErr = mapError("inspect deleted volume", inspectErr)
	if sandbox.Is(inspectErr, sandbox.NotFound) {
		return nil
	}
	if inspectErr != nil {
		return inspectErr
	}
	return failure("delete volume", sandbox.Uncertain, fmt.Errorf("volume still exists after delete"))
}
