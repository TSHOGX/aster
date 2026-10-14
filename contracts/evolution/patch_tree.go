package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ApplyTree computes an isolated candidate from an exact captured base. Artifact
// bytes must still be fetched under current grants and verified before committing.
// Directory removal is recursive; overlapping destructive operations are rejected
// so operation ordering cannot silently change the submitted meaning.
func (p Patch) ApplyTree(output OutputBinding, base []manifest.Entry) ([]manifest.Entry, error) {
	if err := p.Validate(output); err != nil {
		return nil, err
	}
	if err := manifest.ValidateAuthoring(output.Profile, base); err != nil {
		return nil, err
	}
	if manifest.TreeDigest(base) != p.Base.TreeDigest {
		return nil, fmt.Errorf("patch base tree mismatch")
	}
	tree := map[string]manifest.Entry{}
	for _, e := range base {
		tree[e.Path] = e
	}
	for _, operation := range p.Entries {
		before, exists := tree[operation.Path]
		destructive := operation.Operation == "remove" || (exists && before.Kind == "directory" && operation.Kind == "file")
		if destructive {
			for _, other := range p.Entries {
				if strings.HasPrefix(other.Path, operation.Path+"/") {
					return nil, fmt.Errorf("overlapping destructive patch operations")
				}
			}
		}
		if operation.Operation == "remove" && (!exists || before.Kind != operation.Kind) {
			return nil, fmt.Errorf("patch removal does not match base entry")
		}
	}
	// Validation above reads only the immutable base, never partially applied edits.
	for _, operation := range p.Entries {
		if operation.Operation == "remove" {
			for name := range tree {
				if name == operation.Path || strings.HasPrefix(name, operation.Path+"/") {
					delete(tree, name)
				}
			}
			continue
		}
		if operation.Kind == "file" {
			for name := range tree {
				if strings.HasPrefix(name, operation.Path+"/") {
					delete(tree, name)
				}
			}
			tree[operation.Path] = manifest.Entry{Path: operation.Path, Kind: "file", Size: strconv.FormatInt(operation.Artifact.Size, 10), Digest: operation.Artifact.Digest}
		} else {
			tree[operation.Path] = manifest.Entry{Path: operation.Path, Kind: "directory", Size: "0"}
		}
	}
	result := make([]manifest.Entry, 0, len(tree))
	for _, entry := range tree {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	if err := manifest.ValidateAuthoring(output.Profile, result); err != nil {
		return nil, err
	}
	return result, nil
}
