package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
)

const StandardUsersGroup = "standard-users"
const standardUsersPolicy = "standard-user-policy"

type BootstrapSeed struct {
	Resources   ResourceSeed
	Environment DefaultEnvironment
	Sandbox     RegisteredSandbox
	Volumes     []BuiltinVolume
	Network     NetworkPolicy
}

// NewBootstrapSeed constructs the initial selection from current, validated
// operator declarations. It is not an importer and is never run over saved data.
func NewBootstrapSeed(resources ResourceSeed, release RegisteredSandbox, volumes []BuiltinVolume, network NetworkPolicy) (BootstrapSeed, error) {
	if len(resources.AdditionalOwned) != 0 {
		return BootstrapSeed{}, ErrInvalid
	}
	environment := DefaultEnvironment{Revision: 1, SandboxRelease: release.Reference, Mounts: []EnvironmentMount{
		{Key: "workspace", Role: "workspace", Destination: release.Manifest.Workspace, Storage: "owned", Access: "read_write", Source: MountSource{Kind: "empty"}, Capacity: MountCapacity{Setting: ResourceWorkspace}},
		{Key: "runtime", Role: "runtime", Destination: release.Manifest.RuntimeState, Storage: "owned", Access: "read_write", Source: MountSource{Kind: "empty"}, Capacity: MountCapacity{Setting: ResourceRuntime}},
	}}
	for _, artifact := range volumes {
		if len(artifact.Roles) != 1 {
			return BootstrapSeed{}, ErrInvalid
		}
		role := artifact.Roles[0]
		destination := ""
		switch role {
		case "agent_config":
			destination = "/opt/agent-config"
		case "shared_assets":
			destination = "/opt/shared-assets"
		default:
			return BootstrapSeed{}, ErrInvalid
		}
		environment.Mounts = append(environment.Mounts, EnvironmentMount{Key: role, Role: role, Destination: destination, Storage: "shared_readonly", Access: "read_only", Source: MountSource{Kind: "builtin", ArtifactID: artifact.Reference.ID, Version: artifact.Reference.Version, Digest: artifact.Reference.Digest}})
	}
	if resources.Mounts != 0 && resources.Mounts != int64(len(environment.Mounts)) {
		return BootstrapSeed{}, ErrInvalid
	}
	resources.Mounts = int64(len(environment.Mounts))
	seed := BootstrapSeed{resources, environment, release, volumes, network}
	return seed, seed.Validate()
}
func (s BootstrapSeed) Validate() error {
	if err := s.Sandbox.Reference.validate(); err != nil {
		return err
	}
	if s.Sandbox.Reference != s.Environment.SandboxRelease || s.Sandbox.StoreID == "" || !validID(s.Sandbox.TemplateID) || s.Environment.Revision != 1 {
		return ErrInvalid
	}
	manifest, err := runtimerelease.Bytes(s.Sandbox.Manifest)
	if err != nil {
		return err
	}
	if _, err = runtimerelease.Load(bytes.NewReader(manifest), s.Sandbox.Reference.Digest); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, artifact := range s.Volumes {
		if err := artifact.Reference.validate(); err != nil {
			return err
		}
		if len(artifact.Roles) != 1 || artifact.PublicationPurpose == "config" && artifact.Roles[0] != "agent_config" || artifact.PublicationPurpose == "assets" && artifact.Roles[0] != "shared_assets" {
			return ErrInvalid
		}
		key := artifact.Reference.ID + "/" + artifact.Reference.Version
		if seen[key] || artifact.StoreID != s.Sandbox.StoreID || artifact.MinimumBytes <= 0 || (artifact.PublicationPurpose != "config" && artifact.PublicationPurpose != "assets") || len(artifact.Roles) != 1 || artifact.CleanRuntime {
			return ErrInvalid
		}
		seen[key] = true
		if artifact.Reference.Digest != content.Digest(artifact.Entries) {
			return ErrInvalid
		}
		var total int64
		paths := map[string]bool{}
		for _, entry := range artifact.Entries {
			if paths[entry.Path] || entry.Directory && (entry.Size != 0 || entry.Digest != "") || !entry.Directory && !content.ValidDigest(entry.Digest) {
				return ErrInvalid
			}
			paths[entry.Path] = true
			if err := content.ValidPath(entry.Path); err != nil {
				return err
			}
			if entry.Size < 0 || entry.Size > content.MaxFileBytes {
				return ErrInvalid
			}
			total, err = checkedSum(total, entry.Size)
			if err != nil {
				return err
			}
		}
		if total > artifact.MinimumBytes || total > content.MaxTotalBytes || len(artifact.Entries) > content.MaxEntries {
			return ErrInvalid
		}
	}
	resolved, err := ResolveEnvironmentStructure(s.Environment, s.Volumes, s.Resources.Workspace, s.Resources.Runtime, s.Resources.MaxMounts)
	if err != nil {
		return err
	}
	if int64(len(resolved.MountPlan)) != s.Resources.Mounts {
		return ErrInvalid
	}
	var additional []int64
	for _, mount := range resolved.MountPlan {
		if mount.Storage == "owned" && mount.Role == "data" {
			additional = append(additional, mount.CapacityBytes)
		}
	}
	if len(additional) != len(s.Resources.AdditionalOwned) {
		return ErrInvalid
	}
	for i, v := range additional {
		if v != s.Resources.AdditionalOwned[i] {
			return ErrInvalid
		}
	}
	if _, _, err = s.Resources.Resolve(); err != nil {
		return err
	}
	network, err := NewNetworkPolicy(Reachability{Direct: s.Network.Direct, Resolvers: s.Network.Resolvers})
	if err != nil {
		return err
	}
	if network.Revision != s.Network.Revision {
		return ErrInvalid
	}
	return nil
}
func (s *Store) insertBootstrapSeed(ctx context.Context, tx *sql.Tx, seed BootstrapSeed) error {
	// All external input, filesystem and manifest validation precedes the write.
	now := s.timestamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_groups(id,name,description,created_at,updated_at) VALUES(?,?,?, ?,?)`, StandardUsersGroup, StandardUsersGroup, "Ordinary own-project operations", now, now); err != nil {
		return err
	}
	statements, err := json.Marshal(standardStatements())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO permission_policies(id,name,description,statements,created_at,updated_at) VALUES(?,?,?,?,?,?)`, standardUsersPolicy, "Standard user operations", "Public sharing and administrative content access are not granted", string(statements), now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO group_policy_bindings(group_id,policy_id) VALUES(?,?)`, StandardUsersGroup, standardUsersPolicy); err != nil {
		return err
	}
	group, platform, err := seed.Resources.Resolve()
	if err != nil {
		return err
	}
	for _, dimension := range resourceDimensions {
		entitlement := group[dimension]
		if _, err = tx.ExecContext(ctx, `INSERT INTO group_resource_policy(group_id,dimension,default_value,ceiling) VALUES(?,?,?,?)`, StandardUsersGroup, string(dimension), entitlement.Default, entitlement.Ceiling); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO platform_resource_limits(dimension,maximum) VALUES(?,?)`, string(dimension), platform[dimension]); err != nil {
			return err
		}
	}
	release := seed.Sandbox
	manifest, err := json.Marshal(release.Manifest)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO registered_sandboxes(id,version,digest,store_id,template_id,manifest,created_at) VALUES(?,?,?,?,?,?,?)`, release.Reference.ID, release.Reference.Version, release.Reference.Digest, release.StoreID, release.TemplateID, string(manifest), now); err != nil {
		return err
	}
	for _, artifact := range seed.Volumes {
		roles, err := json.Marshal(artifact.Roles)
		if err != nil {
			return err
		}
		entries, err := json.Marshal(artifact.Entries)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO builtin_volumes(id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, artifact.Reference.ID, artifact.Reference.Version, artifact.Reference.Digest, artifact.StoreID, artifact.PublicationPurpose, artifact.MinimumBytes, string(roles), artifact.CleanRuntime, string(entries), now); err != nil {
			return err
		}
	}
	environment, err := json.Marshal(seed.Environment)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO default_environment(id,revision,document,updated_at) VALUES(1,1,?,?)`, string(environment), now); err != nil {
		return err
	}
	network, err := json.Marshal(seed.Network)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO network_defaults(id,revision,policy,accepted_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,policy=excluded.policy,accepted_at=excluded.accepted_at`, seed.Network.Revision, string(network), now)
	return err
}
