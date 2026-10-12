package product

import (
	"errors"
	"math"
	"testing"
)

func resourceSeedFixture() ResourceSeed {
	return ResourceSeed{CPU: 1000, Memory: 1 << 30, Overlay: 256 << 20, Workspace: 20, Runtime: 10, AdditionalOwned: []int64{30}, Mounts: 6, MaxMounts: 8, ProjectSlots: 4, AgentSlots: 32, TerminalSlots: 32}
}
func TestResourceSeedAccountsForCompletePlanAndOverflow(t *testing.T) {
	group, platform, err := resourceSeedFixture().Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for d, want := range map[ResourceDimension]ResourceEntitlement{ResourceVolume: {30, 120}, ResourceProjectVolumes: {60, 240}, ResourcePersistent: {600, 6000}, ResourceMounts: {6, 8}, ResourceRunningProjects: {2, 4}, ResourceAggregateCPU: {2000, 16000}} {
		if group[d] != want || platform[d] != want.Ceiling {
			t.Fatal(d, group[d], platform[d])
		}
	}
	for _, mutate := range []func(*ResourceSeed){func(s *ResourceSeed) { s.CPU = math.MaxInt64 }, func(s *ResourceSeed) { s.Workspace = math.MaxInt64 }, func(s *ResourceSeed) { s.AdditionalOwned = []int64{math.MaxInt64} }, func(s *ResourceSeed) { s.Memory = math.MaxInt64 / 8 }, func(s *ResourceSeed) { s.MaxMounts = 5 }, func(s *ResourceSeed) { s.Runtime = 0 }} {
		seed := resourceSeedFixture()
		mutate(&seed)
		if _, _, err := seed.Resolve(); !errors.Is(err, ErrInvalid) {
			t.Fatal(seed, err)
		}
	}
}
func TestResourceResolutionMaximaInheritanceAndRestoration(t *testing.T) {
	_, platform, err := resourceSeedFixture().Resolve()
	if err != nil {
		t.Fatal(err)
	}
	platform[ResourceCPU] = 10
	groups := []GroupResources{{ID: "low", Dimensions: map[ResourceDimension]ResourceEntitlement{ResourceCPU: {2, 3}}}, {ID: "high", Dimensions: map[ResourceDimension]ResourceEntitlement{ResourceCPU: {7, 12}}}}
	requested := int64(9)
	overrides := map[ResourceDimension]*int64{ResourceCPU: &requested}
	values, err := ResolveResources(groups, platform, nil)
	if err != nil || values[ResourceCPU].Effective != 7 || values[ResourceCPU].Ceiling != 10 {
		t.Fatal(values, err)
	}
	values, err = ResolveResources(groups, platform, overrides)
	if err != nil || values[ResourceCPU].Effective != 9 || values[ResourceCPU].Capped {
		t.Fatal(values, err)
	}
	platform[ResourceCPU] = 5
	values, err = ResolveResources(groups, platform, overrides)
	if err != nil || values[ResourceCPU].Effective != 5 || !values[ResourceCPU].Capped || *values[ResourceCPU].Override != 9 {
		t.Fatal(values, err)
	}
	platform[ResourceCPU] = 10
	values, err = ResolveResources(groups, platform, overrides)
	if err != nil || values[ResourceCPU].Effective != 9 || values[ResourceCPU].Capped {
		t.Fatal(values, err)
	}
	zero := int64(0)
	overrides[ResourceCPU] = &zero
	values, err = ResolveResources(groups, platform, overrides)
	if err != nil || values[ResourceCPU].Effective != 0 {
		t.Fatal(values, err)
	}
	overrides[ResourceCPU] = nil
	values, err = ResolveResources(groups, platform, overrides)
	if err != nil || values[ResourceCPU].Effective != 7 {
		t.Fatal(values, err)
	}
	values, err = ResolveResources(nil, platform, nil)
	if err != nil || values[ResourceCPU].Effective != 0 || values[ResourceCPU].Ceiling != 0 {
		t.Fatal(values, err)
	}
}
