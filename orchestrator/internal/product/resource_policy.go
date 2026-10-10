package product

import (
	"math"
	"sort"
)

// ResourceDimension is a closed set of integer maximum allowances. Allocation
// requests and caps use the same merge rule, but caps never resize a mount plan.
type ResourceDimension string

const (
	ResourceProjects        ResourceDimension = "projects"
	ResourceRunningProjects ResourceDimension = "running_projects"
	ResourceCPU             ResourceDimension = "cpu_millis"
	ResourceMemory          ResourceDimension = "memory_bytes"
	ResourceOverlay         ResourceDimension = "overlay_bytes"
	ResourceWorkspace       ResourceDimension = "workspace_bytes"
	ResourceRuntime         ResourceDimension = "runtime_bytes"
	ResourceAggregateCPU    ResourceDimension = "aggregate_cpu_millis"
	ResourceAggregateMemory ResourceDimension = "aggregate_memory_bytes"
	ResourceVolume          ResourceDimension = "owned_volume_bytes"
	ResourceProjectVolumes  ResourceDimension = "project_owned_volume_bytes"
	ResourceMounts          ResourceDimension = "mount_count"
	ResourcePersistent      ResourceDimension = "persistent_bytes"
	ResourceAgents          ResourceDimension = "agents"
	ResourceTerminals       ResourceDimension = "terminals"
	ResourceSchedules       ResourceDimension = "schedules"
	ResourceTransfer        ResourceDimension = "transfer_bytes"
)

var resourceDimensions = []ResourceDimension{ResourceProjects, ResourceRunningProjects, ResourceCPU, ResourceMemory, ResourceOverlay, ResourceWorkspace, ResourceRuntime, ResourceAggregateCPU, ResourceAggregateMemory, ResourceVolume, ResourceProjectVolumes, ResourceMounts, ResourcePersistent, ResourceAgents, ResourceTerminals, ResourceSchedules, ResourceTransfer}

type ResourceEntitlement struct {
	Default int64 `json:"default"`
	Ceiling int64 `json:"ceiling"`
}
type GroupResources struct {
	ID         string                                    `json:"id"`
	Name       string                                    `json:"name"`
	Revision   int64                                     `json:"revision"`
	Dimensions map[ResourceDimension]ResourceEntitlement `json:"dimensions"`
}
type ResourceSource struct {
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name"`
	Default   int64  `json:"default"`
	Ceiling   int64  `json:"ceiling"`
}
type EffectiveResource struct {
	Default       int64            `json:"default"`
	Ceiling       int64            `json:"ceiling"`
	PlatformLimit int64            `json:"platform_limit"`
	Override      *int64           `json:"override"`
	Effective     int64            `json:"effective"`
	Capped        bool             `json:"capped"`
	Sources       []ResourceSource `json:"sources"`
}

func validResourceDimension(d ResourceDimension) bool {
	for _, known := range resourceDimensions {
		if d == known {
			return true
		}
	}
	return false
}
func ValidateGroupResources(values map[ResourceDimension]ResourceEntitlement) error {
	for d, value := range values {
		if !validResourceDimension(d) || value.Default < 0 || value.Ceiling < value.Default {
			return Admission("resource_invalid", ErrInvalid)
		}
	}
	return nil
}

// ResolveResources preserves explicit requests after a ceiling reduction. A
// later increase can restore their effective values without rewriting settings.
func ResolveResources(groups []GroupResources, platform map[ResourceDimension]int64, overrides map[ResourceDimension]*int64) (map[ResourceDimension]EffectiveResource, error) {
	for _, g := range groups {
		if err := ValidateGroupResources(g.Dimensions); err != nil {
			return nil, err
		}
	}
	for d, v := range platform {
		if !validResourceDimension(d) || v < 0 {
			return nil, ErrInvalid
		}
	}
	for d, v := range overrides {
		if !validResourceDimension(d) || v != nil && *v < 0 {
			return nil, ErrInvalid
		}
	}
	result := make(map[ResourceDimension]EffectiveResource, len(resourceDimensions))
	for _, d := range resourceDimensions {
		limit, ok := platform[d]
		if !ok {
			return nil, Admission("platform_limit_missing", ErrInvalid)
		}
		value := EffectiveResource{PlatformLimit: limit, Override: overrides[d], Sources: []ResourceSource{}}
		for _, g := range groups {
			if offer, ok := g.Dimensions[d]; ok {
				value.Ceiling = max(value.Ceiling, offer.Ceiling)
				value.Default = max(value.Default, offer.Default)
				value.Sources = append(value.Sources, ResourceSource{g.ID, g.Name, offer.Default, offer.Ceiling})
			}
		}
		sort.Slice(value.Sources, func(i, j int) bool { return value.Sources[i].GroupID < value.Sources[j].GroupID })
		value.Ceiling = min(value.Ceiling, limit)
		value.Default = min(value.Default, value.Ceiling)
		value.Effective = value.Default
		if value.Override != nil {
			requested := *value.Override
			value.Override = &requested
			value.Effective = min(requested, value.Ceiling)
			value.Capped = requested > value.Ceiling
		}
		result[d] = value
	}
	return result, nil
}
func checkedProduct(a, b int64) (int64, error) {
	if a < 0 || b < 0 || b != 0 && a > math.MaxInt64/b {
		return 0, Admission("resource_overflow", ErrInvalid)
	}
	return a * b, nil
}
func checkedSum(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, Admission("resource_overflow", ErrInvalid)
	}
	return a + b, nil
}

// ResourceSeed uses validated current declarations. Startup never recomputes
// these values over an administrator's saved settings.
type ResourceSeed struct {
	CPU             int64
	Memory          int64
	Overlay         int64
	Workspace       int64
	Runtime         int64
	AdditionalOwned []int64
	Mounts          int64
	MaxMounts       int64
	ProjectSlots    int64
	AgentSlots      int64
	TerminalSlots   int64
}

func (s ResourceSeed) Resolve() (map[ResourceDimension]ResourceEntitlement, map[ResourceDimension]int64, error) {
	if s.CPU <= 0 || s.Memory <= 0 || s.Overlay <= 0 || s.Workspace <= 0 || s.Runtime <= 0 || s.ProjectSlots <= 0 || s.AgentSlots <= 0 || s.TerminalSlots <= 0 || s.Mounts < int64(2+len(s.AdditionalOwned)) || s.MaxMounts < s.Mounts {
		return nil, nil, ErrInvalid
	}
	total, err := checkedSum(s.Workspace, s.Runtime)
	if err != nil {
		return nil, nil, err
	}
	largest := max(s.Workspace, s.Runtime)
	for _, capacity := range s.AdditionalOwned {
		if capacity <= 0 {
			return nil, nil, ErrInvalid
		}
		total, err = checkedSum(total, capacity)
		if err != nil {
			return nil, nil, err
		}
		largest = max(largest, capacity)
	}
	values := map[ResourceDimension]ResourceEntitlement{
		ResourceProjects: {10, 100}, ResourceRunningProjects: {min(2, s.ProjectSlots), s.ProjectSlots},
		ResourceMounts: {s.Mounts, s.MaxMounts}, ResourceAgents: {min(8, s.AgentSlots), s.AgentSlots}, ResourceTerminals: {min(8, s.TerminalSlots), s.TerminalSlots}, ResourceSchedules: {20, 100}, ResourceTransfer: {256 << 20, 256 << 20},
	}
	for d, v := range map[ResourceDimension]int64{ResourceCPU: s.CPU, ResourceMemory: s.Memory, ResourceOverlay: s.Overlay, ResourceWorkspace: s.Workspace, ResourceRuntime: s.Runtime, ResourceVolume: largest, ResourceProjectVolumes: total} {
		ceiling, err := checkedProduct(v, 4)
		if err != nil {
			return nil, nil, err
		}
		values[d] = ResourceEntitlement{v, ceiling}
	}
	for d, v := range map[ResourceDimension]int64{ResourceAggregateCPU: s.CPU, ResourceAggregateMemory: s.Memory} {
		ceiling, err := checkedProduct(v, 4)
		if err != nil {
			return nil, nil, err
		}
		ceiling, err = checkedProduct(ceiling, s.ProjectSlots)
		if err != nil {
			return nil, nil, err
		}
		initial, err := checkedProduct(v, 2)
		if err != nil {
			return nil, nil, err
		}
		values[d] = ResourceEntitlement{min(initial, ceiling), ceiling}
	}
	initial, err := checkedProduct(total, 10)
	if err != nil {
		return nil, nil, err
	}
	ceiling, err := checkedProduct(total, 100)
	if err != nil {
		return nil, nil, err
	}
	values[ResourcePersistent] = ResourceEntitlement{initial, ceiling}
	platform := make(map[ResourceDimension]int64, len(values))
	for d, v := range values {
		platform[d] = v.Ceiling
	}
	return values, platform, nil
}
