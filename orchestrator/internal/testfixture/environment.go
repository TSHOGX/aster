package testfixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sqlitex"
)

type fixtureSource struct {
	Reference    struct{ ID, Version, Digest string }
	VolumeID     string `json:"volume_id"`
	MinimumBytes int64  `json:"minimum_bytes"`
	CleanRuntime bool   `json:"clean_runtime"`
}
type fixtureVolume struct {
	Key, Role, Destination, Storage, Access string
	Capacity                                int64 `json:"capacity_bytes"`
	Source                                  *fixtureSource
}
type fixtureProfile struct {
	StoreID    string `json:"store_id"`
	TemplateID string `json:"template_id"`
	CPU        int64  `json:"cpu_millis"`
	Memory     int64  `json:"memory_bytes"`
	Disk       int64  `json:"disk_bytes"`
	Volumes    []fixtureVolume
}

// SetRuntime explicitly authors isolated fixture configuration through its owning
// transaction (or offline before opening the owner). No production path calls it.
// Existing registrations remain so fixtures can test retained versions.
func SetRuntime(ctx context.Context, tx *sql.Tx, raw []byte, manifest runtimerelease.Manifest, network []byte, sourceEntries map[string][]content.Entry) error {
	var profile fixtureProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return err
	}
	manifestRaw, err := runtimerelease.Bytes(manifest)
	if err != nil {
		return err
	}
	digest, err := runtimerelease.Digest(manifestRaw)
	if err != nil {
		return err
	}
	version := fmt.Sprintf("fixture-%x", sha256.Sum256(append(append([]byte{}, raw...), manifestRaw...)))
	now := sqlitex.FormatTime(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO registered_sandboxes(id,version,digest,store_id,template_id,manifest,created_at) VALUES('fixture-runtime',?,?,?,?,?,?) ON CONFLICT(id,version) DO NOTHING`, version, digest, profile.StoreID, profile.TemplateID, string(manifestRaw), now); err != nil {
		return err
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT revision FROM environment_templates WHERE id='fixture-environment'),0)+1`).Scan(&revision); err != nil {
		return err
	}
	mounts := []map[string]any{}
	defaults := map[string]int64{"cpu_millis": profile.CPU, "memory_bytes": profile.Memory, "overlay_bytes": profile.Disk}
	for _, volume := range profile.Volumes {
		capacity := map[string]any{}
		source := map[string]any{"kind": "empty"}
		if volume.Storage == "owned" {
			switch volume.Role {
			case "workspace", "runtime":
				dimension := volume.Role + "_bytes"
				capacity["setting"] = dimension
				defaults[dimension] = volume.Capacity
			default:
				capacity["bytes"] = fmt.Sprint(volume.Capacity)
			}
		}
		if volume.Source != nil {
			artifact := volume.Source
			entries := sourceEntries[artifact.Reference.Digest]
			if content.Digest(entries) != artifact.Reference.Digest {
				return fmt.Errorf("explicit fixture source entries required for %s", artifact.Reference.ID)
			}
			purpose := "builtin"
			if volume.Role == "shared_assets" {
				purpose = "shared_assets"
			}
			if volume.Role == "agent_config" {
				purpose = "config"
			}
			roles, _ := json.Marshal([]string{volume.Role})
			encoded, _ := json.Marshal(entries)
			if _, err = tx.ExecContext(ctx, `INSERT INTO builtin_volumes(id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,created_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id,version) DO NOTHING`, artifact.Reference.ID, artifact.Reference.Version, artifact.Reference.Digest, profile.StoreID, purpose, artifact.MinimumBytes, string(roles), artifact.CleanRuntime, string(encoded), now); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO content_publications(store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'published',?,?) ON CONFLICT(store_id,purpose,revision_digest) DO NOTHING`, profile.StoreID, purpose, artifact.Reference.Digest, artifact.MinimumBytes, string(encoded), "fixture-staging-"+artifact.VolumeID, artifact.VolumeID, artifact.VolumeID, now, now); err != nil {
				return err
			}
			source = map[string]any{"kind": "builtin", "artifact_id": artifact.Reference.ID, "version": artifact.Reference.Version, "digest": artifact.Reference.Digest}
		}
		mounts = append(mounts, map[string]any{"key": volume.Key, "role": volume.Role, "destination": volume.Destination, "storage": volume.Storage, "access": volume.Access, "source": source, "capacity": capacity})
	}
	document, _ := json.Marshal(map[string]any{"revision": revision, "sandbox_release": map[string]string{"id": "fixture-runtime", "version": version, "digest": digest}, "mounts": mounts})
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_store(id,store_id) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET store_id=excluded.store_id`, profile.StoreID); err != nil {
		return err
	}
	for dimension, value := range defaults {
		if _, err = tx.ExecContext(ctx, `UPDATE group_resource_policy SET default_value=? WHERE group_id='fixture-users' AND dimension=?`, value, dimension); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_templates(id,name,document,created_at,updated_at) VALUES('fixture-environment','Fixture environment',?,?,?) ON CONFLICT(id) DO UPDATE SET document=excluded.document,revision=revision+1`, string(document), now, now); err != nil {
		return err
	}
	if len(network) > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO network_templates(id,name,policy,created_at,updated_at) VALUES('fixture-network','Fixture network',?,?,?) ON CONFLICT(id) DO UPDATE SET policy=excluded.policy,revision=revision+1`, string(network), now, now); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO policy_resource_bindings(policy_id,resource_kind,resource_id) VALUES('fixture-policy','network_template','fixture-network'),('fixture-policy','environment_template','fixture-environment')`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO group_configuration_defaults(group_id,network_template_id,environment_template_id) VALUES('fixture-users','fixture-network','fixture-environment')`); err != nil {
		return err
	}

	// Fixture setup may occur after metadata creation. Explicitly author the
	// unbound test project's selection; production template edits never do this.
	if _, err = tx.ExecContext(ctx, `UPDATE project_configuration SET environment=? WHERE project_id NOT IN (SELECT project_id FROM project_resources) AND environment_template_id='fixture-environment'`, string(document)); err != nil {
		return err
	}
	if len(network) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE project_configuration SET network=? WHERE project_id NOT IN (SELECT project_id FROM project_resources) AND network_template_id='fixture-network'`, string(network)); err != nil {
			return err
		}
	}

	return nil
}
func seedDefaultRuntime(ctx context.Context, tx *sql.Tx) error {
	profile := fixtureProfile{StoreID: "engine", TemplateID: "test", CPU: 1000, Memory: 256 << 20, Disk: 128 << 20, Volumes: []fixtureVolume{{Key: "workspace", Role: "workspace", Destination: "/workspace", Storage: "owned", Access: "read_write", Capacity: 64 << 20}, {Key: "runtime", Role: "runtime", Destination: "/runtime", Storage: "owned", Access: "read_write", Capacity: 32 << 20}}}
	raw, _ := json.Marshal(profile)
	manifest, _ := RuntimeManifest()
	policy := struct {
		Revision  string `json:"revision"`
		Direct    []any  `json:"direct"`
		Resolvers []any  `json:"resolvers"`
	}{Direct: []any{}, Resolvers: []any{}}
	encoded, _ := json.Marshal(policy)
	policy.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
	encoded, _ = json.Marshal(policy)
	return SetRuntime(ctx, tx, raw, manifest, encoded, nil)
}
