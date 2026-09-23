package khora

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
	"strings"
)

// Preserve leading/trailing whitespace through the pinned engine's cleanRel,
// which trims the complete input before cleaning path components.
func volumePath(name string) string {
	if name != strings.TrimSpace(name) {
		return "./" + name + "/."
	}
	return name
}
func (b *Backend) ImportVolume(ctx context.Context, ref sandbox.VolumeReference, name string, body io.Reader, size int64) error {
	if err := b.validateVolumeRead(ctx, ref, name); err != nil {
		return err
	}
	if size < 0 || size > 256<<20 {
		return failure("volume import", sandbox.Invalid, fmt.Errorf("bounded content required"))
	}
	if size == 0 {
		return b.WriteVolume(ctx, ref, name, nil)
	}
	result, err := b.client.ImportVolume(ctx, ref.VolumeID, volumePath(name), body)
	if err != nil {
		return mapError("volume import", err)
	}
	if result.VolumeID != ref.VolumeID || result.BytesWritten != size {
		return failure("volume import", sandbox.Uncertain, fmt.Errorf("incomplete volume import"))
	}
	return nil
}
func (b *Backend) MoveVolume(ctx context.Context, ref sandbox.VolumeReference, source, destination string) error {
	if err := b.validateVolumeRead(ctx, ref, source); err != nil {
		return err
	}
	if err := b.validateVolumeRead(ctx, ref, destination); err != nil {
		return err
	}
	_, err := b.client.MoveVolume(ctx, ref.VolumeID, volumePath(source), volumePath(destination))
	return mapError("volume move", err)
}
func (b *Backend) RemoveVolumePath(ctx context.Context, ref sandbox.VolumeReference, name string) error {
	if err := b.validateVolumeRead(ctx, ref, name); err != nil {
		return err
	}
	return mapError("volume remove", b.client.RemoveVolumePath(ctx, ref.VolumeID, volumePath(name)))
}

var _ sandbox.VolumeFileMutations = (*Backend)(nil)
