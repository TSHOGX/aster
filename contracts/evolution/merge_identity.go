package evolution

import (
	"encoding/json"
	"fmt"
	"sort"

	manifest "aster/contracts/volumemanifest"
)

func (c CheckRequirement) Validate() error {
	if !manifest.ValidDigest(c.Digest) || !manifest.ValidID(c.PolicyID) || c.PolicyRevision < 1 || manifest.ValidPath(c.Path, true) != nil {
		return fmt.Errorf("invalid check policy identity")
	}
	if c.Mode != "required" && c.Mode != "report_only" {
		return fmt.Errorf("disabled evaluation is not a check")
	}
	if err := c.Profile.Validate(); err != nil {
		return err
	}
	if err := c.Service.Validate(); err != nil {
		return err
	}
	switch c.ReferenceStrategy {
	case "none":
		if c.Reference != nil {
			return fmt.Errorf("absolute evaluation has no reference")
		}
	case "generation_base", "target_main", "pinned_release":
		if c.Reference == nil {
			return fmt.Errorf("resolved reference required")
		}
		if err := c.Reference.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown reference strategy")
	}
	identity, err := c.IdentityDigest()
	if err != nil {
		return err
	}
	if identity != c.Digest {
		return fmt.Errorf("check digest does not match policy contents")
	}
	return nil
}

// ApprovalDigest binds the complete integration result, ordered Git parents and
// current policy. Check order is cosmetic; source order defines merge parents.
func (p PreparedIdentity) ApprovalDigest() (string, error) {
	if !manifest.ValidID(p.LibraryID) || p.PolicyRevision < 1 || !manifest.ValidDigest(p.PolicyDigest) || len(p.Sources) == 0 {
		return "", fmt.Errorf("incomplete prepared identity")
	}
	for _, ref := range append([]ContentRef{p.Main, p.Result}, p.Sources...) {
		if err := ref.Validate(); err != nil {
			return "", err
		}
		if ref.LibraryID != p.LibraryID || ref.Profile != p.Main.Profile {
			return "", fmt.Errorf("integration scope mismatch")
		}
	}
	seen := map[string]bool{p.Main.CommitID: true}
	sourceHeads := map[string]bool{}
	for _, source := range p.Sources {
		if seen[source.CommitID] {
			return "", fmt.Errorf("duplicate integration parent")
		}
		seen[source.CommitID] = true
		sourceHeads[source.CommitID] = true
	}
	if len(p.Selections) != 0 && len(p.Selections) != len(p.Sources) {
		return "", fmt.Errorf("prepared selections do not cover sources")
	}
	selections := map[string]bool{}
	for i := range p.Selections {
		selection := &p.Selections[i]
		if !manifest.ValidID(selection.BranchID) || !ObjectID(selection.Head) || !sourceHeads[selection.Head] || len(selection.SelectedPaths) == 0 {
			return "", fmt.Errorf("invalid prepared selection")
		}
		if selections[selection.BranchID] {
			return "", fmt.Errorf("duplicate prepared selection")
		}
		selections[selection.BranchID] = true
		seenPaths := map[string]bool{}
		for _, path := range selection.SelectedPaths {
			if manifest.ValidPath(path, true) != nil || seenPaths[path] {
				return "", fmt.Errorf("invalid selected path")
			}
			seenPaths[path] = true
		}
		for _, path := range selection.DeferredPaths {
			if manifest.ValidPath(path, true) != nil || seenPaths[path] {
				return "", fmt.Errorf("invalid deferred path")
			}
			seenPaths[path] = true
		}
	}
	copy := p
	copy.Checks = append([]CheckRequirement{}, p.Checks...)
	copy.Selections = append([]PreparedSelection{}, p.Selections...)
	for i := range copy.Selections {
		copy.Selections[i].SelectedPaths = append([]string{}, copy.Selections[i].SelectedPaths...)
		copy.Selections[i].DeferredPaths = append([]string{}, copy.Selections[i].DeferredPaths...)
		sort.Strings(copy.Selections[i].SelectedPaths)
		sort.Strings(copy.Selections[i].DeferredPaths)
	}
	sort.Slice(copy.Selections, func(i, j int) bool { return copy.Selections[i].BranchID < copy.Selections[j].BranchID })
	checks := map[string]bool{}
	for _, check := range copy.Checks {
		if err := check.Validate(); err != nil {
			return "", err
		}
		if checks[check.Digest] {
			return "", fmt.Errorf("duplicate check identity")
		}
		checks[check.Digest] = true
		if check.ReferenceStrategy == "target_main" && *check.Reference != p.Main {
			return "", fmt.Errorf("target-main reference differs from prepared main")
		}
	}
	sort.Slice(copy.Checks, func(i, j int) bool { return copy.Checks[i].Digest < copy.Checks[j].Digest })
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return Digest(raw), nil
}
