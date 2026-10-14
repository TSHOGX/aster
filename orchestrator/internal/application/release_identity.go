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
	"harness-platform/orchestrator/internal/servicegateway"
)

const releaseIdentityVersion = 1

// ReleaseIdentity binds a candidate's executable to its declared compatibility
// inputs. Matching compatibility is necessary, not sufficient, for a live owner
// handoff: admission, durable work and stable gateway checks remain mandatory.

type ReleaseIdentity struct {
	Version             int    `json:"version"`
	ExecutableDigest    string `json:"executable_digest"`
	SchemaVersion       int    `json:"schema_version"`
	SchemaDigest        string `json:"schema_digest"`
	ConfigurationDigest string `json:"configuration_digest"`
	OwnerProtocol       int    `json:"owner_protocol"`
	PlatformProtocol    int    `json:"platform_protocol"`
	GatewayProtocol     int    `json:"gateway_protocol"`
	CompatibilityDigest string `json:"compatibility_digest"`
}

type preparedPublication struct {
	purpose  string
	revision content.Revision
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
	for _, source := range []struct{ path, purpose, profile string }{{cfg.Content.AgentConfig, "config", content.ReviewedProfile}, {cfg.Content.SharedAssets, "shared_assets", content.SharedAssetsProfile}} {
		if source.path == "" {
			continue
		}
		revision, err := content.ReadProfile(source.path, source.profile)
		if err != nil {
			return nil, err
		}
		out = append(out, preparedPublication{source.purpose, revision})
	}
	return out, nil
}

// InspectRelease measures the executable and host configuration only. Bootstrap
// manifests and authored sources belong to explicit operator actions. Persisted
// registry/environment authority is fenced separately under the product lock.
func InspectRelease(cfg Config) (id ReleaseIdentity, err error) {
	if err = cfg.Validate(); err != nil {
		return id, err
	}
	executable, err := executableDigest()
	if err != nil {
		return id, err
	}
	id = ReleaseIdentity{Version: releaseIdentityVersion, PlatformProtocol: servicegateway.PlatformVersion, OwnerProtocol: product.OwnerHandoffProtocol, ExecutableDigest: executable, GatewayProtocol: servicegateway.AuthorityVersion}
	id.SchemaVersion, id.SchemaDigest, err = product.SchemaIdentity()
	if err != nil {
		return id, err
	}
	id.ConfigurationDigest, err = digestJSON(struct {
		Config Config
		Base   string
	}{fixedConfiguration(cfg), cfg.base})
	if err != nil {
		return id, err
	}
	contract := id
	contract.ExecutableDigest = ""
	id.CompatibilityDigest, err = digestJSON(contract)
	return id, err
}
