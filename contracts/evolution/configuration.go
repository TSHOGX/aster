package evolution

import (
	"encoding/json"
	"fmt"
	"math"

	manifest "aster/contracts/volumemanifest"
)

func (r ReleaseRef) Validate() error {
	if !manifest.ValidID(r.PublisherID) || !manifest.ValidID(r.LibraryID) || !manifest.ValidID(r.VersionID) || !manifest.ValidDigest(r.DescriptorDigest) {
		return fmt.Errorf("invalid immutable release reference")
	}
	return nil
}
func (p EvaluationProfile) Validate() error {
	for _, ref := range append([]Revision{p.Ref, p.Scorer, p.Runtime, p.Model}, p.Tools...) {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	if err := p.Dataset.Validate(); err != nil {
		return err
	}
	if len(p.Thresholds) == 0 {
		return fmt.Errorf("evaluation thresholds required")
	}
	seen := map[string]bool{}
	for _, m := range p.Thresholds {
		if !manifest.ValidID(m.Name) || seen[m.Name] || m.Unit == "" || m.MinimumSamples < 1 || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			return fmt.Errorf("invalid metric threshold")
		}
		seen[m.Name] = true
		if m.Operator != "gte" && m.Operator != "lte" {
			return fmt.Errorf("unsupported threshold operator")
		}
	}
	return nil
}

// Validate checks a resolved activation projection. Incomplete drafts are stored
// separately; central authority and catalog compatibility are additional checks.
func (c Configuration) Validate() error {
	if !manifest.ValidID(c.GroupID) || c.Revision < 1 {
		return fmt.Errorf("invalid configuration identity")
	}
	if err := c.Budget.Validate(); err != nil {
		return err
	}
	sources := map[string]SourceBinding{}
	for _, s := range c.Sources {
		if !manifest.ValidID(s.ID) || !manifest.ValidID(s.ResourceID) || s.PolicyRevision < 1 {
			return fmt.Errorf("invalid source identity")
		}
		if _, ok := sources[s.ID]; ok {
			return fmt.Errorf("duplicate source binding")
		}
		if s.Kind != "volume" && s.Kind != "project" && s.Kind != "session" {
			return fmt.Errorf("unsupported source kind")
		}
		audience := map[string]bool{}
		for _, id := range s.AudienceGroups {
			if !manifest.ValidID(id) || audience[id] {
				return fmt.Errorf("invalid source audience")
			}
			audience[id] = true
		}
		for _, p := range s.Exclusions {
			if err := manifest.ValidPath(p, false); err != nil {
				return err
			}
		}
		sources[s.ID] = s
	}
	bindings := map[string]Binding{}
	for i, b := range c.Bindings {
		if !manifest.ValidID(b.ID) {
			return fmt.Errorf("invalid binding identity")
		}
		if _, ok := bindings[b.ID]; ok {
			return fmt.Errorf("duplicate binding")
		}
		bindings[b.ID] = b
		for _, ref := range []Revision{b.Template, b.Workflow, b.Service} {
			if err := ref.Validate(); err != nil {
				return err
			}
		}
		if err := b.Output.Validate(); err != nil {
			return err
		}
		if err := b.Evaluation.Validate(); err != nil {
			return err
		}
		if err := b.Budget.Validate(); err != nil {
			return err
		}
		if b.Budget.MaxCostMicros > c.Budget.MaxCostMicros || b.Budget.MaxTokens > c.Budget.MaxTokens || b.Budget.MaxArtifactBytes > c.Budget.MaxArtifactBytes || b.Budget.MaxAttempts > c.Budget.MaxAttempts || b.Budget.TimeoutSeconds > c.Budget.TimeoutSeconds {
			return fmt.Errorf("binding budget exceeds group ceiling")
		}
		var params map[string]json.RawMessage
		if json.Unmarshal(b.Parameters, &params) != nil || params == nil {
			return fmt.Errorf("parameters must be an object")
		}
		switch b.Trigger {
		case "manual", "event":
			if b.Schedule != "" {
				return fmt.Errorf("unexpected schedule")
			}
		case "schedule":
			if b.Schedule == "" {
				return fmt.Errorf("schedule required")
			}
		default:
			return fmt.Errorf("unsupported trigger")
		}
		if len(b.SourceIDs) == 0 {
			return fmt.Errorf("binding requires explicit sources")
		}
		seen := map[string]bool{}
		resources := map[[2]string]bool{}
		for _, id := range b.SourceIDs {
			s, ok := sources[id]
			if !ok || seen[id] {
				return fmt.Errorf("missing or duplicate source")
			}
			seen[id] = true
			resource := [2]string{s.Kind, s.ResourceID}
			if resources[resource] {
				return fmt.Errorf("binding selects the same source resource more than once")
			}
			resources[resource] = true
			if !s.DerivativePublication {
				return fmt.Errorf("source does not authorize derivative publication")
			}
			allowed := false
			for _, group := range s.AudienceGroups {
				if group == c.GroupID {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("source audience excludes owning group")
			}
		}
		for _, other := range c.Bindings[:i] {
			if b.Output.LibraryID == other.Output.LibraryID && PrefixesOverlap(b.Output.Prefix, other.Output.Prefix) {
				return fmt.Errorf("overlapping output ownership")
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("binding dependency cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		b := bindings[id]
		seen := map[string]bool{}
		for _, dep := range b.DependsOn {
			other, ok := bindings[dep]
			if !ok || seen[dep] || b.Enabled && !other.Enabled {
				return fmt.Errorf("invalid binding dependency")
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range bindings {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
