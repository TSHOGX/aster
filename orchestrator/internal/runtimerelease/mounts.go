package runtimerelease

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MountProtocol = 1

type RequiredPublication struct {
	Role        string `json:"role"`
	Destination string `json:"destination"`
	ArtifactID  string `json:"artifact_id"`
}

// MountContract records trusted image path metadata. Paths are guest paths;
// no engine or build-host handles cross this contract. BlockedPaths contains
// image files and symlinks outside protected subtrees, so mount validation never
// needs to start a sandbox or resolve a potentially different host filesystem.
type MountContract struct {
	Protocol             int                   `json:"protocol"`
	ProtectedRoots       []string              `json:"protected_roots"`
	BlockedPaths         []string              `json:"blocked_paths"`
	RequiredPublications []RequiredPublication `json:"required_publications"`
}

var mountRole = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var protectedMountRoots = []string{"/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/tmp", "/usr", "/var", "/opt/node-v" + NodeVersion}

func ValidGuestMountPath(value string) bool {
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
func mountPathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func DefaultMountContract() MountContract {
	return MountContract{Protocol: MountProtocol, ProtectedRoots: slices.Clone(protectedMountRoots), BlockedPaths: []string{}, RequiredPublications: []RequiredPublication{{Role: "agent_config", Destination: "/opt/agent-config", ArtifactID: "agent_config"}, {Role: "shared_assets", Destination: "/opt/shared-assets", ArtifactID: "shared_assets"}}}
}
func (m MountContract) Validate() error {
	if m.Protocol != MountProtocol || len(m.ProtectedRoots) > 128 || len(m.BlockedPaths) > 10000 || len(m.RequiredPublications) > 32 || m.BlockedPaths == nil || m.RequiredPublications == nil {
		return errors.New("invalid runtime mount contract")
	}
	for _, root := range protectedMountRoots {
		if !slices.Contains(m.ProtectedRoots, root) {
			return fmt.Errorf("missing protected runtime path %s", root)
		}
	}
	for _, list := range [][]string{m.ProtectedRoots, m.BlockedPaths} {
		seen := map[string]bool{}
		for _, entry := range list {
			if !ValidGuestMountPath(entry) || seen[entry] {
				return errors.New("invalid runtime path metadata")
			}
			seen[entry] = true
		}
	}
	seen := map[string]bool{}
	for i, publication := range m.RequiredPublications {
		if !mountRole.MatchString(publication.Role) || publication.Role == "workspace" || publication.Role == "runtime" || publication.Role == "data" || !mountRole.MatchString(publication.ArtifactID) || seen[publication.Role] {
			return errors.New("invalid required publication role")
		}
		seen[publication.Role] = true
		if !ValidGuestMountPath(publication.Destination) {
			return errors.New("invalid required publication destination")
		}
		for _, root := range append(slices.Clone(m.ProtectedRoots), m.BlockedPaths...) {
			if mountPathsOverlap(root, publication.Destination) {
				return errors.New("required publication conflicts with runtime image")
			}
		}
		for _, other := range m.RequiredPublications[:i] {
			if mountPathsOverlap(other.Destination, publication.Destination) {
				return errors.New("overlapping required publications")
			}
		}
	}
	return nil
}
func (m MountContract) Publication(role string) (RequiredPublication, bool) {
	for _, publication := range m.RequiredPublications {
		if publication.Role == role {
			return publication, true
		}
	}
	return RequiredPublication{}, false
}
func (m MountContract) ValidateDestination(role, destination string) error {
	if !ValidGuestMountPath(destination) {
		return errors.New("canonical absolute guest mount path required")
	}
	if required, ok := m.Publication(role); ok {
		if required.Destination != destination {
			return errors.New("required publication cannot move")
		}
	} else if role != "workspace" && role != "runtime" && role != "data" {
		return errors.New("role not declared by selected runtime")
	}
	for _, root := range m.ProtectedRoots {
		if mountPathsOverlap(destination, root) {
			return errors.New("mount overlaps protected runtime path")
		}
	}
	for _, blocked := range m.BlockedPaths {
		if mountPathsOverlap(destination, blocked) {
			return errors.New("mount overlaps an image file or symlink")
		}
	}
	for _, required := range m.RequiredPublications {
		if required.Role != role && mountPathsOverlap(destination, required.Destination) {
			return errors.New("mount overlaps required publication")
		}
	}
	return nil
}
func generateMountContract(root string) (MountContract, error) {
	contract := DefaultMountContract()
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if file == root {
			return nil
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		guest := "/" + filepath.ToSlash(relative)
		for _, protected := range contract.ProtectedRoots {
			if guest == protected || strings.HasPrefix(guest, protected+"/") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !entry.IsDir() {
			contract.BlockedPaths = append(contract.BlockedPaths, guest)
			if len(contract.BlockedPaths) > 10000 {
				return errors.New("runtime path metadata exceeds bound")
			}
		}
		return nil
	})
	if err != nil {
		return contract, err
	}
	slices.Sort(contract.BlockedPaths)
	return contract, contract.Validate()
}
