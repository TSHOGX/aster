package khora

import (
	"context"
	"fmt"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/sandbox"

	sdk "github.com/TSHOGX/khora/pkg/khora"
)

// maxWriteChunk bounds one direct volume write. The engine holds its write-regime
// marker for the whole request, so a single unbounded body would park cold starts
// on that volume for as long as the transfer takes.
const maxWriteChunk = content.MaxFileBytes

// MkdirVolume creates one directory, tolerating an existing one.
//
// The engine answers 409 for a path that already exists, which makes the raw verb
// non-idempotent. A resumed preparation replays the whole tree onto the same
// keyed staging volume, so the caller would otherwise be unable to retry at all.
// An existing entry of the wrong type is still a conflict, since the directory
// this call promised does not exist.
func (b *Backend) MkdirVolume(ctx context.Context, reference sandbox.VolumeReference, name string) error {
	if err := b.validateVolumeRead(ctx, reference, name); err != nil {
		return err
	}
	_, err := b.client.MkdirVolume(ctx, reference.VolumeID, volumePath(name), 0o755)
	if err == nil {
		return nil
	}
	mapped := mapError("volume mkdir", err)
	if !sandbox.Is(mapped, sandbox.Conflict) {
		return mapped
	}
	existing, statErr := b.client.LstatVolume(ctx, reference.VolumeID, volumePath(name))
	if statErr != nil {
		return mapped
	}
	if existing.Type != "directory" {
		return failure("volume mkdir", sandbox.Conflict, fmt.Errorf("%q already exists as %s", name, existing.Type))
	}
	return nil
}

func (b *Backend) WriteVolume(ctx context.Context, reference sandbox.VolumeReference, name string, data []byte) error {
	if err := b.validateVolumeRead(ctx, reference, name); err != nil {
		return err
	}
	if len(data) > maxWriteChunk {
		return failure("volume write", sandbox.Invalid, fmt.Errorf("volume write exceeds %d bytes", maxWriteChunk))
	}
	result, err := b.client.WriteVolume(ctx, reference.VolumeID, volumePath(name), 0, data)
	if err != nil {
		return mapError("volume write", err)
	}
	// A short write is not a partial success to retry blindly: the engine replaces
	// the target before streaming, so the path now holds neither the old nor the
	// intended content and only a full rewrite of this entry can settle it.
	if result.VolumeID != reference.VolumeID || result.BytesWritten != int64(len(data)) {
		return failure("volume write", sandbox.Uncertain, fmt.Errorf("engine wrote %d of %d bytes", result.BytesWritten, len(data)))
	}
	return nil
}

// SnapshotVolume derives an immutable published volume from prepared content.
func (b *Backend) SnapshotVolume(ctx context.Context, source sandbox.VolumeReference, key string, capacity int64) (sandbox.Volume, error) {
	if err := b.validateVolumeRead(ctx, source, "root"); err != nil {
		return sandbox.Volume{}, err
	}
	if key == "" || capacity <= 0 {
		return sandbox.Volume{}, failure("volume snapshot", sandbox.Invalid, fmt.Errorf("durable key and positive capacity required"))
	}
	id, err := b.client.SnapshotVolumeWithKey(ctx, source.VolumeID, capacity, sdk.IdempotencyKey(key))
	if err != nil {
		return sandbox.Volume{}, mapError("volume snapshot", err)
	}
	if id == "" {
		return sandbox.Volume{}, failure("volume snapshot", sandbox.Uncertain, fmt.Errorf("engine omitted derived volume identity"))
	}
	// A replayed key can name an object that was since deleted, so a 2xx is not
	// evidence the volume exists. Only a current inspection establishes that.
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Volume{}, err
	}
	volume, err := b.client.Volume(ctx, id)
	if err != nil {
		mapped := mapError("inspect volume", err)
		if sandbox.Is(mapped, sandbox.NotFound) {
			// Preserve the exact identity returned by the original idempotent
			// operation. Cleanup can distinguish observed absence from a source
			// error or an ambiguous failure that did not identify an allocation.
			if _, identityErr := b.Identity(ctx); identityErr != nil {
				return sandbox.Volume{}, identityErr
			}
			return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: b.identity.StoreID, VolumeID: id}, Capacity: capacity}, mapped
		}
		return sandbox.Volume{}, mapped
	}
	if volume.VolumeID != id || volume.Size != capacity {
		return sandbox.Volume{}, failure("inspect volume", sandbox.Uncertain, fmt.Errorf("derived volume observation does not match intent"))
	}
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: b.identity.StoreID, VolumeID: volume.VolumeID}, Capacity: volume.Size, UsedBytes: volume.UsedByte}, nil
}

var (
	_ sandbox.VolumeWrites     = (*Backend)(nil)
	_ sandbox.VolumeDerivation = (*Backend)(nil)
)
