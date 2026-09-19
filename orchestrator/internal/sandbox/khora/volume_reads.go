package khora

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
	"time"
)

func (b *Backend) validateVolumeRead(ctx context.Context, reference sandbox.VolumeReference, name string) error {
	identity, err := b.Identity(ctx)
	if err != nil {
		return err
	}
	if reference.StoreID != identity.StoreID {
		return failure("volume read", sandbox.IdentityChanged, fmt.Errorf("volume belongs to another engine store"))
	}
	if reference.VolumeID == "" || strings.ContainsAny(reference.VolumeID, "/\\?#") || name == "" || strings.ContainsAny(name, "\\\x00") {
		return failure("volume read", sandbox.Invalid, fmt.Errorf("volume identity and clean relative path required"))
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return failure("volume read", sandbox.Invalid, fmt.Errorf("clean relative volume path required"))
		}
	}
	return nil
}
func (b *Backend) LstatVolume(ctx context.Context, reference sandbox.VolumeReference, name string) (sandbox.VolumeEntry, error) {
	if err := b.validateVolumeRead(ctx, reference, name); err != nil {
		return sandbox.VolumeEntry{}, err
	}
	entry, err := b.client.LstatVolume(ctx, reference.VolumeID, name)
	if err != nil {
		return sandbox.VolumeEntry{}, mapError("volume lstat", err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, entry.ModTime)
	if err != nil {
		return sandbox.VolumeEntry{}, failure("volume lstat", sandbox.Incompatible, err)
	}
	if entry.Size < 0 {
		return sandbox.VolumeEntry{}, failure("volume lstat", sandbox.Incompatible, fmt.Errorf("negative volume file size"))
	}
	return sandbox.VolumeEntry{Name: entry.Name, Type: entry.Type, Size: entry.Size, ModifiedAt: stamp}, nil
}
func (b *Backend) ReadVolume(ctx context.Context, reference sandbox.VolumeReference, name string, offset, length int64) ([]byte, error) {
	if err := b.validateVolumeRead(ctx, reference, name); err != nil {
		return nil, err
	}
	if offset < 0 || length <= 0 || length > 256<<10 {
		return nil, failure("volume read", sandbox.Invalid, fmt.Errorf("bounded positive volume range required"))
	}
	data, err := b.client.ReadVolume(ctx, reference.VolumeID, name, offset, length)
	if err != nil {
		return nil, mapError("volume read", err)
	}
	if int64(len(data)) > length {
		return nil, failure("volume read", sandbox.Incompatible, fmt.Errorf("volume response exceeded requested range"))
	}
	return data, nil
}
