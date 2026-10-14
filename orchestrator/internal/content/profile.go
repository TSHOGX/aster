package content

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

const ReviewedProfile = "reviewed-v1"
const AuthoredProfile = "authored-v1"
const SharedAssetsProfile = "shared-assets-v1"

type Limits struct {
	Entries              int
	FileBytes, TreeBytes int64
}

func ProfileLimits(profile string) (Limits, error) {
	switch profile {
	case SharedAssetsProfile:
		return Limits{MaxEntries, MaxFileBytes, MaxTotalBytes}, nil
	case ReviewedProfile:
		return Limits{MaxEntries, MaxFileBytes, MaxTotalBytes}, nil
	case AuthoredProfile:
		return Limits{10000, 256 << 20, 1 << 30}, nil
	default:
		return Limits{}, fmt.Errorf("unsupported content profile %q", profile)
	}
}

// Purpose persists the source's trust category alongside every publication and
// registry entry. Authored data can never inherit reviewed system capabilities.
func ProfileForPurpose(purpose string) string {
	if purpose == "authored" {
		return AuthoredProfile
	}
	return ReviewedProfile
}
func ValidateEntries(entries []Entry, profile string) (int64, error) {
	limits, err := ProfileLimits(profile)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 || len(entries) > limits.Entries {
		return 0, fmt.Errorf("content entry count outside profile")
	}
	if profile == SharedAssetsProfile {
		hasAgents := false
		for _, e := range entries {
			if e.Path == "AGENTS.md" && !e.Directory {
				hasAgents = true
			}
			if strings.HasPrefix(e.Path, "skills/") && !e.Directory && path.Base(e.Path) != "SKILL.md" {
				return 0, fmt.Errorf("shared-assets skill files must be SKILL.md")
			}
		}
		if !hasAgents {
			return 0, fmt.Errorf("shared-assets profile requires root AGENTS.md")
		}
	}
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	parents := map[string]bool{}
	var total int64
	for i, e := range sorted {
		if err := ValidPath(e.Path); err != nil {
			return 0, err
		}
		if i > 0 && sorted[i-1].Path == e.Path {
			return 0, fmt.Errorf("duplicate content path")
		}
		if p := path.Dir(e.Path); p != "." && !parents[p] {
			return 0, fmt.Errorf("explicit directory parent required")
		}
		if e.Size < 0 || e.Size > limits.FileBytes || e.Size > limits.TreeBytes-total {
			return 0, fmt.Errorf("content exceeds profile bytes")
		}
		total += e.Size
		if e.Directory {
			if e.Size != 0 || e.Digest != "" || strings.TrimSpace(path.Base(e.Path)) != path.Base(e.Path) {
				return 0, fmt.Errorf("invalid directory metadata")
			}
			parents[e.Path] = true
		} else if !ValidDigest(e.Digest) {
			return 0, fmt.Errorf("invalid file digest")
		}
	}
	return total, nil
}

// MaterializationCapacity applies checked bounded headroom to an authored tree.
// It is a reservation policy, not a promise about physical CoW block usage.
func MaterializationCapacity(entries []Entry) (int64, error) {
	total, err := ValidateEntries(entries, AuthoredProfile)
	if err != nil {
		return 0, err
	}
	footprint := total + int64(len(entries))*(16<<10)
	headroom := (footprint + 9) / 10
	if headroom < 16<<20 {
		headroom = 16 << 20
	}
	capacity := footprint + headroom
	if capacity < 64<<20 {
		capacity = 64 << 20
	}
	return ((capacity + (64 << 20) - 1) / (64 << 20)) * (64 << 20), nil
}
