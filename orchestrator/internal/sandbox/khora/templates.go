package khora

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
)

func (b *Backend) RequireTemplate(ctx context.Context, id string) error {
	if id == "" || len(id) > 128 || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#\x00") {
		return failure("template", sandbox.Invalid, fmt.Errorf("invalid template identity"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return err
	}
	template, err := b.client.Template(ctx, id)
	if err != nil {
		return mapError("template", err)
	}
	if template.TemplateID != id {
		return failure("template", sandbox.Conflict, fmt.Errorf("template identity mismatch"))
	}
	return nil
}

func (b *Backend) InspectVolume(ctx context.Context, ref sandbox.VolumeReference) (sandbox.Volume, error) {
	if ref.VolumeID == "" || ref.StoreID != b.identity.StoreID {
		return sandbox.Volume{}, failure("inspect volume", sandbox.Invalid, fmt.Errorf("original volume identity required"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Volume{}, err
	}
	volume, err := b.client.Volume(ctx, ref.VolumeID)
	if err != nil {
		return sandbox.Volume{}, mapError("inspect volume", err)
	}
	if _, err = b.Identity(ctx); err != nil {
		return sandbox.Volume{}, err
	}
	if volume.VolumeID != ref.VolumeID {
		return sandbox.Volume{}, failure("inspect volume", sandbox.Conflict, fmt.Errorf("volume identity mismatch"))
	}
	return sandbox.Volume{VolumeReference: ref, Capacity: volume.Size, UsedBytes: volume.UsedByte}, nil
}
