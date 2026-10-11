package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"harness-platform/orchestrator/internal/runtimerelease"
)

var ErrArtifactUnavailable = errors.New("registered runtime artifact unavailable")

// RuntimeBindings contains operator-owned reachability only. Allocation sizes,
// mounts and release selection are database authority, never caller defaults.
type RuntimeBindings struct {
	StoreID  string
	Services []ServiceRule
	Hosts    map[string]string
}

func (p Profile) Bindings() RuntimeBindings {
	return RuntimeBindings{StoreID: p.StoreID, Services: p.Reachability.Services, Hosts: p.Reachability.Hosts}
}
func (b RuntimeBindings) validate() error {
	if b.StoreID == "" {
		return ErrInvalid
	}
	return (Reachability{Services: b.Services, Hosts: b.Hosts}).validate()
}
func registeredSandbox(ctx context.Context, q queryer, reference ArtifactReference, newAllocation bool) (RegisteredSandbox, error) {
	var out RegisteredSandbox
	var manifest, retired string
	var enabled bool
	err := missing(q.QueryRowContext(ctx, `SELECT id,version,digest,store_id,template_id,manifest,enabled,retired_at FROM registered_sandboxes WHERE id=? AND version=?`, reference.ID, reference.Version).Scan(&out.Reference.ID, &out.Reference.Version, &out.Reference.Digest, &out.StoreID, &out.TemplateID, &manifest, &enabled, &retired))
	if errors.Is(err, ErrNotFound) {
		return out, ErrArtifactUnavailable
	}
	if err != nil {
		return out, err
	}
	if out.Reference != reference || retired != "" || newAllocation && !enabled {
		return out, ErrArtifactUnavailable
	}
	out.Manifest, err = runtimerelease.Load(bytes.NewReader([]byte(manifest)), reference.Digest)
	if err != nil {
		return out, Admission("runtime_incompatible", ErrConflict)
	}
	return out, nil
}
func resolveAllocationProfile(ctx context.Context, q queryer, actor string, bindings RuntimeBindings) (Profile, error) {
	if err := bindings.validate(); err != nil {
		return Profile{}, err
	}
	settings, err := userResourceSettings(ctx, q, actor)
	if err != nil {
		return Profile{}, err
	}
	environment, artifacts, err := currentEnvironment(ctx, q)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Profile{}, ErrArtifactUnavailable
		}
		return Profile{}, err
	}
	release, err := registeredSandbox(ctx, q, environment.SandboxRelease, true)
	if err != nil {
		return Profile{}, err
	}
	if release.StoreID != bindings.StoreID {
		return Profile{}, Admission("engine_store_mismatch", ErrConflict)
	}
	value := func(d ResourceDimension) int64 { return settings.Effective[d].Effective }
	for _, dimension := range []ResourceDimension{ResourceCPU, ResourceMemory, ResourceOverlay, ResourceWorkspace, ResourceRuntime} {
		if value(dimension) <= 0 {
			return Profile{}, &QuotaError{Dimension: dimension, RequestedDelta: 1, Limit: value(dimension)}
		}
	}
	plan, err := ResolveEnvironmentStructure(environment, release.Manifest.Mounts, artifacts, value(ResourceWorkspace), value(ResourceRuntime), settings.Effective[ResourceMounts].PlatformLimit)
	if err != nil {
		return Profile{}, err
	}
	profile := Profile{EnvironmentRevision: environment.Revision, ResourceRevision: settings.AuthorizationRevision, SandboxRelease: release.Reference, ReleaseDigest: release.Reference.Digest, StoreID: release.StoreID, TemplateID: release.TemplateID, CPUMillis: value(ResourceCPU), MemoryBytes: value(ResourceMemory), DiskBytes: value(ResourceOverlay), Reachability: Reachability{Services: bindings.Services, Hosts: bindings.Hosts}, Volumes: []VolumePlan{}}
	for _, mount := range plan.MountPlan {
		slot := VolumePlan{Key: mount.Key, Role: mount.Role, Destination: mount.Destination, Storage: mount.Storage, Access: mount.Access, CapacityBytes: mount.CapacityBytes}
		if mount.Source.Kind == "builtin" {
			var artifact BuiltinVolume
			found := false
			for _, candidate := range artifacts {
				if candidate.Reference == mount.Source.Reference() {
					artifact = candidate
					found = true
					break
				}
			}
			if !found || artifact.StoreID != bindings.StoreID {
				return Profile{}, ErrArtifactUnavailable
			}
			var enabled bool
			if err = q.QueryRowContext(ctx, `SELECT enabled AND retired_at='' FROM builtin_volumes WHERE id=? AND version=? AND digest=?`, artifact.Reference.ID, artifact.Reference.Version, artifact.Reference.Digest).Scan(&enabled); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return Profile{}, ErrArtifactUnavailable
				}
				return Profile{}, err
			}
			if !enabled {
				return Profile{}, ErrArtifactUnavailable
			}
			var volume string
			var capacity int64
			if err = q.QueryRowContext(ctx, `SELECT published_volume_id,capacity FROM content_publications WHERE store_id=? AND purpose=? AND revision_digest=? AND stage='published'`, artifact.StoreID, artifact.PublicationPurpose, artifact.Reference.Digest).Scan(&volume, &capacity); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return Profile{}, ErrArtifactUnavailable
				}
				return Profile{}, err
			}
			slot.Source = &VolumeSource{Reference: artifact.Reference, VolumeID: volume, MinimumBytes: artifact.MinimumBytes, CleanRuntime: artifact.CleanRuntime}
			if slot.Storage == "shared_readonly" {
				slot.CapacityBytes = capacity
			}
		}
		profile.Volumes = append(profile.Volumes, slot)
	}
	profile, _, err = selectedNetworkProfile(ctx, q, actor, profile)
	if err != nil {
		return Profile{}, err
	}
	return canonicalProfile(profile), profile.Validate()
}
func (s *Store) RetainedRuntime(ctx context.Context, profile Profile) (RegisteredSandbox, error) {
	return registeredSandbox(ctx, s.reader, profile.SandboxRelease, false)
}

// Stored environment revision and body must agree. A torn or manually forged
// document is not silently reinterpreted as the current revision.
func decodeEnvironment(raw string, revision int64) (DefaultEnvironment, error) {
	var out DefaultEnvironment
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, err
	}
	if out.Revision != revision {
		return out, ErrConflict
	}
	return out, nil
}

// validateRetainedProfile checks an immutable plan against its own registered
// image contract. Registry defaults never substitute for a retained release.
func validateRetainedProfile(ctx context.Context, q queryer, profile Profile) error {
	release, err := registeredSandbox(ctx, q, profile.SandboxRelease, false)
	if err != nil {
		return err
	}
	if release.StoreID != profile.StoreID || release.TemplateID != profile.TemplateID || release.Reference.Digest != profile.ReleaseDigest {
		return Admission("runtime_incompatible", ErrConflict)
	}
	if err = profile.Validate(); err != nil {
		return err
	}
	roles := map[string]int{}
	for _, mount := range profile.Volumes {
		roles[mount.Role]++
		if err = release.Manifest.Mounts.ValidateDestination(mount.Role, mount.Destination); err != nil {
			return Admission("runtime_incompatible", ErrConflict)
		}
		if required, ok := release.Manifest.Mounts.Publication(mount.Role); ok {
			if mount.Storage != "shared_readonly" || mount.Access != "read_only" || mount.Source == nil || mount.Source.Reference.ID != required.ArtifactID {
				return Admission("runtime_incompatible", ErrConflict)
			}
		}
	}
	for _, required := range release.Manifest.Mounts.RequiredPublications {
		if roles[required.Role] != 1 {
			return Admission("runtime_incompatible", ErrConflict)
		}
	}
	return nil
}
