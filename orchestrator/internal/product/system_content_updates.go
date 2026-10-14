package product

import (
	"context"
	"slices"

	"harness-platform/orchestrator/internal/content"
)

const systemContentReplaceKind = "system_content_replace"
const pendingContentRolloutReason = "Another content rollout is pending. Settle it before changing a different content type."

func sharedAssetsMounts(e DefaultEnvironment) []EnvironmentMount {
	out := []EnvironmentMount{}
	for _, m := range e.Mounts {
		if m.Role == "shared_assets" {
			out = append(out, m)
		}
	}
	return out
}
func systemContentCompatible(ctx context.Context, q queryer, current DefaultEnvironment, target []EnvironmentMount) (bool, error) {
	before := sharedAssetsMounts(current)
	if len(before) != 1 || len(target) != 1 {
		return false, Admission("update_structure_changed", ErrConflict)
	}
	a, b := before[0], target[0]
	if a.Key != b.Key || a.Destination != b.Destination {
		return false, Admission("update_structure_changed", ErrConflict)
	}
	for _, m := range []EnvironmentMount{a, b} {
		if m.Role != "shared_assets" || m.Storage != "shared_readonly" || m.Access != "read_only" || m.Source.Kind != "builtin" || m.Capacity != (MountCapacity{}) {
			return false, Admission("update_structure_changed", ErrConflict)
		}
		artifact, err := registeredBuiltin(ctx, q, m.Source.Reference(), false)
		if err != nil {
			return false, err
		}
		if artifact.PublicationPurpose != "shared_assets" || !slices.Equal(artifact.Roles, []string{"shared_assets"}) {
			return false, ErrInvalid
		}
		if _, err = content.ValidateEntries(artifact.Entries, content.SharedAssetsProfile); err != nil {
			return false, ErrInvalid
		}
	}
	return a.Source.Reference() != b.Source.Reference(), nil
}
func systemContentPlan(ctx context.Context, q queryer, m EnvironmentMount, store string) (VolumePlan, error) {
	if m.Role != "shared_assets" || m.Storage != "shared_readonly" || m.Access != "read_only" || m.Source.Kind != "builtin" {
		return VolumePlan{}, ErrInvalid
	}
	a, err := registeredBuiltin(ctx, q, m.Source.Reference(), true)
	if err != nil {
		return VolumePlan{}, err
	}
	if a.StoreID != store || a.PublicationPurpose != "shared_assets" || !slices.Equal(a.Roles, []string{"shared_assets"}) {
		return VolumePlan{}, ErrInvalid
	}
	if _, err = content.ValidateEntries(a.Entries, content.SharedAssetsProfile); err != nil {
		return VolumePlan{}, ErrInvalid
	}
	var volume string
	var capacity int64
	err = q.QueryRowContext(ctx, `SELECT published_volume_id,capacity FROM content_publications WHERE store_id=? AND purpose='shared_assets' AND revision_digest=? AND stage='published'`, store, a.Reference.Digest).Scan(&volume, &capacity)
	if err != nil || volume == "" || capacity < a.MinimumBytes {
		return VolumePlan{}, ErrArtifactUnavailable
	}
	return VolumePlan{Key: m.Key, Role: m.Role, Destination: m.Destination, Storage: m.Storage, Access: m.Access, CapacityBytes: capacity, Source: &VolumeSource{ContentProfile: content.SharedAssetsProfile, Reference: a.Reference, VolumeID: volume, MinimumBytes: a.MinimumBytes}}, nil
}

// A selected cohort must not overwrite a prior uncertain operation's intent.
func selectedSystemProject(ctx context.Context, q queryer, project string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM environment_update_executions WHERE project_id=? AND state IN ('pending','unknown')`, project).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return Admission("update_attempt_unsettled", ErrConflict)
	}
	return nil
}
