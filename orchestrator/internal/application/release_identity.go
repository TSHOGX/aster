package application

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/servicegateway"
)

// ReleaseIdentity binds a candidate's executable to its declared compatibility
// inputs. Matching compatibility is necessary, not sufficient, for a live owner
// handoff: admission, durable work and stable gateway checks remain mandatory.
type ReleaseIdentity struct {
	Version               int               `json:"version"`
	ExecutableDigest      string            `json:"executable_digest"`
	SchemaVersion         int               `json:"schema_version"`
	SchemaDigest          string            `json:"schema_digest"`
	ConfigurationDigest   string            `json:"configuration_digest"`
	RuntimeDigest         string            `json:"runtime_digest"`
	ContentDigests        map[string]string `json:"content_digests"`
	RuntimeCatalog        map[string]string `json:"runtime_catalog"`
	OwnerProtocol         int               `json:"owner_protocol"`
	PlatformProtocol      int               `json:"platform_protocol"`
	GatewayProtocol       int               `json:"gateway_protocol"`
	CompatibilityDigest   string            `json:"compatibility_digest"`
	NetworkPolicyRevision string            `json:"network_policy_revision"`
}

type preparedPublication struct {
	purpose, destination string
	revision             content.Revision
}

type preparedRelease struct {
	config       Config
	identity     ReleaseIdentity
	publications []preparedPublication
	templates    map[product.ReleasedTemplate]runtimerelease.Manifest
}

func executableDigest() (string, error) {
	// /proc/self/exe names the running inode even after a deployment path changes.
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, executable)
	if err = errors.Join(err, executable.Close()); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

func digestJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

func readPublications(cfg Config) ([]preparedPublication, error) {
	var out []preparedPublication
	for _, source := range []struct{ path, purpose, destination string }{{cfg.Content.AgentConfig, "config", "/opt/agent-config"}, {cfg.Content.SharedAssets, "assets", "/opt/shared-assets"}} {
		if source.path == "" {
			continue
		}
		revision, err := content.Read(source.path)
		if err != nil {
			return nil, err
		}
		out = append(out, preparedPublication{source.purpose, source.destination, revision})
	}
	return out, nil
}

// InspectRelease reads configuration, reviewed trees and the pinned guest
// manifest. It contacts no engine/provider and opens no product Store/socket.
// Startup uses the same preparation, so its identity names published revisions,
// not an independent second read of possibly changed source files.
func InspectRelease(cfg Config) (ReleaseIdentity, error) {
	prepared, err := prepareRelease(cfg)
	return prepared.identity, err
}

func prepareRelease(cfg Config) (out preparedRelease, err error) {
	if err = cfg.Validate(); err != nil {
		return out, err
	}
	executable, err := executableDigest()
	if err != nil {
		return out, err
	}
	manifestFile, err := os.Open(cfg.Template.Manifest)
	if err != nil {
		return out, err
	}
	cfg.Manifest, err = runtimerelease.Load(manifestFile, cfg.Template.Digest)
	err = errors.Join(err, manifestFile.Close())
	if err != nil {
		return out, err
	}
	out.config = cfg
	out.templates = map[product.ReleasedTemplate]runtimerelease.Manifest{{TemplateID: cfg.Template.ID, Digest: cfg.Template.Digest}: cfg.Manifest}
	for _, setting := range cfg.RetainedTemplates {
		file, e := os.Open(setting.Manifest)
		if e != nil {
			return out, e
		}
		manifest, e := runtimerelease.Load(file, setting.Digest)
		e = errors.Join(e, file.Close())
		if e != nil {
			return out, fmt.Errorf("retained template %s: %w", setting.ID, e)
		}
		out.templates[product.ReleasedTemplate{TemplateID: setting.ID, Digest: setting.Digest}] = manifest
	}
	out.publications, err = readPublications(cfg)
	if err != nil {
		return out, err
	}
	// Version 4 includes the reserved platform protocol and schedule lifecycle.
	id := ReleaseIdentity{Version: 4, PlatformProtocol: servicegateway.PlatformVersion, OwnerProtocol: product.OwnerHandoffProtocol, ExecutableDigest: executable, RuntimeDigest: cfg.Template.Digest, GatewayProtocol: servicegateway.AuthorityVersion, ContentDigests: map[string]string{}}
	policy, err := product.NewNetworkPolicy(cfg.Project.Reachability)
	if err != nil {
		return out, err
	}
	id.NetworkPolicyRevision = policy.Revision
	id.RuntimeCatalog = make(map[string]string, len(out.templates))
	for binding := range out.templates {
		id.RuntimeCatalog[binding.TemplateID] = binding.Digest
	}
	id.SchemaVersion, id.SchemaDigest, err = product.SchemaIdentity()
	if err != nil {
		return out, err
	}
	// Include the base directory because engine credential paths resolve against
	// it. Raw configuration and credentials are never returned by inspection.
	id.ConfigurationDigest, err = digestJSON(struct {
		Config Config
		Base   string
	}{fixedConfiguration(cfg), cfg.base})
	if err != nil {
		return out, err
	}
	for _, source := range out.publications {
		id.ContentDigests[source.purpose] = source.revision.Digest
	}
	// Executable identity is separate: a new build can declare the same contract.
	contract := id
	contract.ExecutableDigest = ""
	contract.NetworkPolicyRevision = ""
	id.CompatibilityDigest, err = digestJSON(contract)
	if err != nil {
		return out, err
	}
	out.identity = id
	return out, nil
}
