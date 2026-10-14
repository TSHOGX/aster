package manifest

import (
	"fmt"
	"strings"
)

// ValidateAuthoring checks storage invariants without requiring a complete,
// publishable profile. A checkpoint may lack AGENTS.md or a skill's SKILL.md;
// publication still calls NewProfile and ValidateContent on the final tree.
func ValidateAuthoring(profile string, entries []Entry) error {
	if !SupportedProfile(profile) {
		return fmt.Errorf("unsupported authoring profile")
	}
	if len(entries) == 0 {
		return nil
	}
	d, err := NewProfile("authoring", "library", "checkpoint", Profile, entries)
	if err != nil {
		return err
	}
	for _, e := range d.Entries {
		for _, component := range strings.Split(e.Path, "/") {
			if strings.EqualFold(component, ".git") {
				return fmt.Errorf("reserved Git path: %s", e.Path)
			}
		}
	}
	if profile == SharedAssetsProfile {
		if len(entries) > SharedAssetsMaxEntries {
			return fmt.Errorf("shared-assets entry limit exceeded")
		}
		total, _ := ParseSize(d.TotalBytes)
		if total > SharedAssetsMaxTreeBytes {
			return fmt.Errorf("shared-assets tree limit exceeded")
		}
		for _, e := range d.Entries {
			n, _ := ParseSize(e.Size)
			if n > SharedAssetsMaxFileBytes {
				return fmt.Errorf("shared-assets file limit exceeded")
			}
		}
	}
	return nil
}
