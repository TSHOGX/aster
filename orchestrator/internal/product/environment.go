package product

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
)

// ArtifactReference identifies immutable application metadata, never a browser
// supplied engine handle. Registration and selecting a default are separate.
type ArtifactReference struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

func (r ArtifactReference) validate() error {
	if !validID(r.ID) || len(r.ID) > 64 || !validText(r.Version, 128) || !content.ValidDigest(r.Digest) {
		return Admission("environment_invalid", ErrInvalid)
	}
	return nil
}

type MountSource struct {
	Kind       string `json:"kind"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Version    string `json:"version,omitempty"`
	Digest     string `json:"digest,omitempty"`
}

func (s MountSource) Reference() ArtifactReference {
	return ArtifactReference{s.ArtifactID, s.Version, s.Digest}
}

type MountCapacity struct {
	Setting ResourceDimension `json:"setting,omitempty"`
	Bytes   int64             `json:"bytes,omitempty"`
}
type EnvironmentMount struct {
	Key         string        `json:"key"`
	Role        string        `json:"role"`
	Destination string        `json:"destination"`
	Storage     string        `json:"storage"`
	Access      string        `json:"access"`
	Source      MountSource   `json:"source"`
	Capacity    MountCapacity `json:"capacity"`
}
type DefaultEnvironment struct {
	Revision       int64              `json:"revision"`
	SandboxRelease ArtifactReference  `json:"sandbox_release"`
	Mounts         []EnvironmentMount `json:"mounts"`
}
type RegisteredSandbox struct {
	Reference  ArtifactReference       `json:"reference"`
	StoreID    string                  `json:"store_id"`
	TemplateID string                  `json:"template_id"`
	Manifest   runtimerelease.Manifest `json:"manifest"`
}
type BuiltinVolume struct {
	Reference          ArtifactReference `json:"reference"`
	StoreID            string            `json:"store_id"`
	PublicationPurpose string            `json:"publication_purpose"`
	MinimumBytes       int64             `json:"minimum_bytes"`
	Roles              []string          `json:"roles"`
	CleanRuntime       bool              `json:"clean_runtime"`
	Entries            []content.Entry   `json:"entries"`
}
type ResolvedMount struct {
	EnvironmentMount
	CapacityBytes int64 `json:"capacity_bytes"`
}
type ResolvedEnvironment struct {
	DefaultEnvironment
	MountPlan         []ResolvedMount `json:"mount_plan"`
	OwnedBytes        int64           `json:"owned_bytes"`
	LargestOwnedBytes int64           `json:"largest_owned_bytes"`
}

func validMountPath(value string) bool {
	if !utf8.ValidString(value) || len(value) > 4096 || value == "/" || !path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func environmentError(field string) error { return Admission("environment_invalid:"+field, ErrInvalid) }

// ResolveEnvironmentStructure checks mount shape and complete-plan accounting.
// Runtime qualification additionally checks the registered release's trusted
// path contract before any allocation. No engine effect occurs here.
func ResolveEnvironmentStructure(environment DefaultEnvironment, artifacts []BuiltinVolume, workspace, runtime, maxMounts int64) (ResolvedEnvironment, error) {
	result := ResolvedEnvironment{DefaultEnvironment: environment, MountPlan: []ResolvedMount{}}
	if err := environment.SandboxRelease.validate(); err != nil {
		return result, err
	}
	if environment.Revision < 1 || workspace <= 0 || runtime <= 0 || maxMounts < 2 || int64(len(environment.Mounts)) > maxMounts {
		return result, environmentError("mounts")
	}
	keys := map[string]bool{}
	roles := map[string]int{}
	for index, mount := range environment.Mounts {
		field := fmt.Sprintf("mounts.%d", index)
		if !validID(mount.Key) || len(mount.Key) > 64 || keys[mount.Key] {
			return result, environmentError(field + ".key")
		}
		keys[mount.Key] = true
		if !validMountPath(mount.Destination) {
			return result, environmentError(field + ".destination")
		}
		for _, other := range result.MountPlan {
			if pathsOverlap(mount.Destination, other.Destination) {
				return result, environmentError(field + ".destination")
			}
		}
		for _, root := range []string{"/proc", "/sys", "/dev", "/etc", "/bin", "/sbin", "/lib", "/lib64", "/usr", "/opt/node-v" + runtimerelease.NodeVersion} {
			if pathsOverlap(mount.Destination, root) {
				return result, environmentError(field + ".destination")
			}
		}
		switch mount.Role {
		case "workspace", "runtime", "data", "agent_config", "shared_assets":
		default:
			return result, environmentError(field + ".role")
		}
		roles[mount.Role]++
		if mount.Role != "data" && roles[mount.Role] > 1 {
			return result, environmentError(field + ".role")
		}
		if mount.Storage != "owned" && mount.Storage != "shared_readonly" {
			return result, environmentError(field + ".storage")
		}
		if mount.Access != "read_write" && mount.Access != "read_only" {
			return result, environmentError(field + ".access")
		}
		var artifact *BuiltinVolume
		switch mount.Source.Kind {
		case "empty":
			if mount.Source.ArtifactID != "" || mount.Source.Version != "" || mount.Source.Digest != "" || mount.Storage != "owned" {
				return result, environmentError(field + ".source")
			}
		case "builtin":
			if err := mount.Source.Reference().validate(); err != nil {
				return result, environmentError(field + ".source")
			}
			for i := range artifacts {
				if artifacts[i].Reference == mount.Source.Reference() {
					artifact = &artifacts[i]
					break
				}
			}
			if artifact == nil {
				return result, Admission("artifact_unavailable", ErrNotFound)
			}
			allowed := false
			for _, role := range artifact.Roles {
				if role == mount.Role {
					allowed = true
				}
			}
			if !allowed || mount.Role == "runtime" && !artifact.CleanRuntime {
				return result, environmentError(field + ".source")
			}
		default:
			return result, environmentError(field + ".source")
		}
		resolved := ResolvedMount{EnvironmentMount: mount}
		if mount.Storage == "shared_readonly" {
			if mount.Access != "read_only" || mount.Source.Kind != "builtin" || mount.Capacity != (MountCapacity{}) {
				return result, environmentError(field + ".capacity")
			}
			resolved.CapacityBytes = artifact.MinimumBytes
		} else {
			switch mount.Role {
			case "workspace":
				if mount.Capacity.Setting != ResourceWorkspace || mount.Capacity.Bytes != 0 || mount.Access != "read_write" {
					return result, environmentError(field + ".capacity")
				}
				resolved.CapacityBytes = workspace
			case "runtime":
				if mount.Capacity.Setting != ResourceRuntime || mount.Capacity.Bytes != 0 || mount.Access != "read_write" {
					return result, environmentError(field + ".capacity")
				}
				resolved.CapacityBytes = runtime
			case "data":
				if mount.Capacity.Setting != "" || mount.Capacity.Bytes <= 0 {
					return result, environmentError(field + ".capacity")
				}
				resolved.CapacityBytes = mount.Capacity.Bytes
			default:
				return result, environmentError(field + ".storage")
			}
			var err error
			result.OwnedBytes, err = checkedSum(result.OwnedBytes, resolved.CapacityBytes)
			if err != nil {
				return result, err
			}
			result.LargestOwnedBytes = max(result.LargestOwnedBytes, resolved.CapacityBytes)
		}
		if (mount.Role == "workspace" || mount.Role == "runtime") && mount.Storage != "owned" {
			return result, environmentError(field + ".storage")
		}
		if artifact != nil && (artifact.MinimumBytes <= 0 || resolved.CapacityBytes < artifact.MinimumBytes) {
			return result, environmentError(field + ".capacity")
		}
		// System publication destinations are part of the current runtime contract.
		systemPath := ""
		switch mount.Role {
		case "agent_config":
			systemPath = "/opt/agent-config"
		case "shared_assets":
			systemPath = "/opt/shared-assets"
		}
		for _, root := range []string{"/opt/agent-config", "/opt/shared-assets"} {
			if pathsOverlap(mount.Destination, root) && mount.Destination != systemPath {
				return result, environmentError(field + ".destination")
			}
		}
		if systemPath != "" && (mount.Destination != systemPath || mount.Storage != "shared_readonly") {
			return result, environmentError(field + ".destination")
		}
		result.MountPlan = append(result.MountPlan, resolved)
	}
	if roles["workspace"] != 1 || roles["runtime"] != 1 {
		return result, environmentError("required_roles")
	}
	return result, nil
}
