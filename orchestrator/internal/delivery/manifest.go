// Package delivery implements version-pinned, host-local release delivery.
package delivery

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var versionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$`)
var accountRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Sandbox struct {
	Reference string   `json:"reference"`
	Archive   Artifact `json:"archive"`
	Pi        string   `json:"pi"`
	Bridge    string   `json:"bridge"`
}
type Manifest struct {
	Format               int                 `json:"format"`
	Version              string              `json:"version"`
	Commit               string              `json:"commit"`
	OS                   string              `json:"os"`
	Arch                 string              `json:"arch"`
	Ubuntu               []string            `json:"ubuntu"`
	Toolchains           map[string]string   `json:"toolchains"`
	Artifacts            map[string]Artifact `json:"artifacts"`
	Sandbox              Sandbox             `json:"sandbox"`
	RunscVersions        []string            `json:"runsc_versions"`
	Schema               map[string]int      `json:"schema"`
	SupportedSources     []string            `json:"supported_sources"`
	Downtime             bool                `json:"downtime"`
	Rollback             string              `json:"rollback"`
	ExternalDependencies []string            `json:"external_dependencies"`
}

func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func ValidVersion(v string) error {
	if !versionRE.MatchString(v) {
		return fmt.Errorf("an exact vMAJOR.MINOR.PATCH[-rc.N] release is required")
	}
	return nil
}
func (a Artifact) Validate() error {
	if filepath.Base(a.Name) != a.Name || a.Name == "." || a.Name == "" || !strings.HasSuffix(a.Name, ".tar.gz") {
		return fmt.Errorf("invalid archive name")
	}
	if a.Size <= 0 || a.Size > 40<<30 || !validHash(a.SHA256) {
		return fmt.Errorf("invalid size or SHA256 for %s", a.Name)
	}
	return nil
}
func (m Manifest) Validate() error {
	if err := ValidVersion(m.Version); err != nil {
		return err
	}
	if m.Format != 1 || m.OS != "linux" || m.Arch != "amd64" || len(m.Commit) != 40 {
		return fmt.Errorf("unsupported release manifest format/platform/commit")
	}
	if _, err := hex.DecodeString(m.Commit); err != nil {
		return err
	}
	if len(m.Artifacts) != 3 {
		return fmt.Errorf("release must contain exactly backend, frontend and deploy archives")
	}
	names := map[string]bool{}
	for _, k := range []string{"backend", "frontend", "deploy"} {
		a, ok := m.Artifacts[k]
		if !ok {
			return fmt.Errorf("missing %s artifact", k)
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if names[a.Name] {
			return fmt.Errorf("duplicate artifact")
		}
		names[a.Name] = true
	}
	if err := m.Sandbox.Archive.Validate(); err != nil {
		return err
	}
	if names[m.Sandbox.Archive.Name] {
		return fmt.Errorf("sandbox artifact name collision")
	}
	parts := strings.Split(m.Sandbox.Reference, "@sha256:")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "ghcr.io/") || !validHash(parts[1]) || strings.ContainsAny(parts[0], "?# ") || strings.Contains(parts[0], "..") {
		return fmt.Errorf("sandbox must reference a GHCR sha256 digest")
	}
	if m.Schema["store"] != 1 || m.Schema["memorystore"] != 1 || len(m.Schema) != 2 {
		return fmt.Errorf("unsupported target schema")
	}
	if len(m.Ubuntu) != 2 || m.Ubuntu[0] != "22.04" || m.Ubuntu[1] != "24.04" {
		return fmt.Errorf("unsupported Ubuntu qualification matrix")
	}
	if !m.Downtime || m.Rollback != "snapshot-before-traffic" || len(m.RunscVersions) == 0 || m.Sandbox.Pi == "" || m.Sandbox.Bridge == "" {
		return fmt.Errorf("missing runtime/upgrade compatibility contract")
	}
	return nil
}
