package content

import (
	"aster/contracts/volumemanifest"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

const ReviewedProfile = "reviewed-v1"
const AuthoredProfile = manifest.Profile
const SharedAssetsProfile = manifest.SharedAssetsProfile

type Limits struct {
	Entries              int
	FileBytes, TreeBytes int64
}

func ProfileLimits(profile string) (Limits, error) {
	switch profile {
	case SharedAssetsProfile:
		return Limits{manifest.SharedAssetsMaxEntries, manifest.SharedAssetsMaxFileBytes, manifest.SharedAssetsMaxTreeBytes}, nil
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
	if purpose == "shared_assets" {
		return SharedAssetsProfile
	}
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
		if _, err := manifest.NewProfile("content", "content", "version", profile, ManifestEntries(entries)); err != nil {
			return 0, err
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
	return MaterializationCapacityForProfile(entries, AuthoredProfile)
}

func MaterializationCapacityForProfile(entries []Entry, profile string) (int64, error) {
	total, err := ValidateEntries(entries, profile)
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

func ManifestEntries(entries []Entry) []manifest.Entry {
	out := make([]manifest.Entry, 0, len(entries))
	for _, e := range entries {
		kind := "file"
		if e.Directory {
			kind = "directory"
		}
		out = append(out, manifest.Entry{Path: e.Path, Kind: kind, Size: strconv.FormatInt(e.Size, 10), Digest: e.Digest})
	}
	return out
}
func (r Revision) ValidateContent() error {
	if r.Profile != SharedAssetsProfile {
		return nil
	}
	d, err := manifest.NewProfile("content", "content", "version", r.Profile, ManifestEntries(r.Entries))
	if err != nil {
		return err
	}
	if r.Reader == nil {
		return fmt.Errorf("shared assets require a verified content reader")
	}
	return d.ValidateContent(r.Reader.ReadFile)
}
