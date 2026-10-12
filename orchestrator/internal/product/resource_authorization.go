package product

import "sort"

// ResourceTemplateRef is a catalog identity, never an engine handle.
type ResourceTemplateRef struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Sources     []string `json:"sources"`
}
type SharedVolumeGrant struct {
	Volume  ResourceTemplateRef `json:"volume"`
	Access  string              `json:"access"`
	Sources []string            `json:"sources"`
}
type EffectiveConfiguration struct {
	Networks             []ResourceTemplateRef `json:"networks"`
	Environments         []ResourceTemplateRef `json:"environments"`
	SharedVolumes        []SharedVolumeGrant   `json:"shared_volumes"`
	DefaultNetwork       *ResourceTemplateRef  `json:"default_network"`
	DefaultEnvironment   *ResourceTemplateRef  `json:"default_environment"`
	NetworkDefaultSource []string              `json:"network_default_source"`
	EnvironmentSources   []string              `json:"environment_sources"`
	NetworkConflict      bool                  `json:"network_conflict"`
	EnvironmentConflict  bool                  `json:"environment_conflict"`
	NeedsAdminResolution bool                  `json:"needs_admin_resolution"`
}
type GroupConfiguration struct {
	ID, Name                           string
	Networks, Environments             []ResourceTemplateRef
	Volumes                            []SharedVolumeGrant
	DefaultNetwork, DefaultEnvironment *ResourceTemplateRef
}

func uniqueSorted(values []string) []string {
	set := map[string]bool{}
	for _, v := range values {
		if v != "" {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func mergeRefs(dst []ResourceTemplateRef, refs ...ResourceTemplateRef) []ResourceTemplateRef {
	byID := map[string]ResourceTemplateRef{}
	for _, v := range append(dst, refs...) {
		if v.ID == "" || !v.Enabled {
			continue
		}
		old := byID[v.ID]
		v.Sources = uniqueSorted(append(old.Sources, v.Sources...))
		byID[v.ID] = v
	}
	out := make([]ResourceTemplateRef, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func selectedDefault(refs []ResourceTemplateRef, defaults map[string][]string) (*ResourceTemplateRef, []string, bool) {
	if len(defaults) > 1 {
		return nil, []string{}, true
	}
	for id, sources := range defaults {
		for _, ref := range refs {
			if ref.ID == id && ref.Enabled {
				return &ref, uniqueSorted(sources), false
			}
		}
	}
	return nil, []string{}, false
}

// ResolveEffectiveConfiguration unions eligible identities, never network rules
// or mount plans. Missing, disabled and conflicting defaults fail closed.
func ResolveEffectiveConfiguration(groups []GroupConfiguration) EffectiveConfiguration {
	out := EffectiveConfiguration{Networks: []ResourceTemplateRef{}, Environments: []ResourceTemplateRef{}, SharedVolumes: []SharedVolumeGrant{}}
	defaultsN, defaultsE := map[string][]string{}, map[string][]string{}
	volumes := map[string]SharedVolumeGrant{}
	for _, g := range groups {
		for _, ref := range g.Networks {
			ref.Sources = append(ref.Sources, g.ID)
			out.Networks = mergeRefs(out.Networks, ref)
		}
		for _, ref := range g.Environments {
			ref.Sources = append(ref.Sources, g.ID)
			out.Environments = mergeRefs(out.Environments, ref)
		}
		if g.DefaultNetwork != nil {
			defaultsN[g.DefaultNetwork.ID] = append(defaultsN[g.DefaultNetwork.ID], g.ID)
		}
		if g.DefaultEnvironment != nil {
			defaultsE[g.DefaultEnvironment.ID] = append(defaultsE[g.DefaultEnvironment.ID], g.ID)
		}
		for _, grant := range g.Volumes {
			if !grant.Volume.Enabled || (grant.Access != "read_only" && grant.Access != "read_write") {
				continue
			}
			current, ok := volumes[grant.Volume.ID]
			if !ok {
				current = grant
				current.Sources = nil
			}
			if grant.Access == "read_write" {
				current.Access = "read_write"
			}
			current.Sources = uniqueSorted(append(append(current.Sources, grant.Sources...), g.ID))
			volumes[grant.Volume.ID] = current
		}
	}
	for _, v := range volumes {
		out.SharedVolumes = append(out.SharedVolumes, v)
	}
	sort.Slice(out.SharedVolumes, func(i, j int) bool { return out.SharedVolumes[i].Volume.ID < out.SharedVolumes[j].Volume.ID })
	out.DefaultNetwork, out.NetworkDefaultSource, out.NetworkConflict = selectedDefault(out.Networks, defaultsN)
	out.DefaultEnvironment, out.EnvironmentSources, out.EnvironmentConflict = selectedDefault(out.Environments, defaultsE)
	out.NeedsAdminResolution = out.NetworkConflict || out.EnvironmentConflict || out.DefaultNetwork == nil || out.DefaultEnvironment == nil
	return out
}
