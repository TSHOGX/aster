package product

import (
	"reflect"
	"testing"
)

func TestConfigurationDefaultsAndGrants(t *testing.T) {
	a := ResourceTemplateRef{ID: "a", Enabled: true}
	b := ResourceTemplateRef{ID: "b", Enabled: true}
	e := ResourceTemplateRef{ID: "e", Enabled: true}
	groups := []GroupConfiguration{
		{ID: "one", Networks: []ResourceTemplateRef{a}, Environments: []ResourceTemplateRef{e}, DefaultNetwork: &a, DefaultEnvironment: &e, Volumes: []SharedVolumeGrant{{Volume: a, Access: "read_only"}}},
		{ID: "two", Networks: []ResourceTemplateRef{b, a}, Environments: []ResourceTemplateRef{e}, DefaultNetwork: &a, DefaultEnvironment: &e, Volumes: []SharedVolumeGrant{{Volume: a, Access: "read_write"}}},
	}
	result := ResolveEffectiveConfiguration(groups)
	if result.NeedsAdminResolution || len(result.Networks) != 2 || result.DefaultNetwork.ID != "a" || result.SharedVolumes[0].Access != "read_write" {
		t.Fatalf("unexpected resolution: %+v", result)
	}
	if !reflect.DeepEqual(result.NetworkDefaultSource, []string{"one", "two"}) {
		t.Fatal(result.NetworkDefaultSource)
	}
	reversed := ResolveEffectiveConfiguration([]GroupConfiguration{groups[1], groups[0]})
	if !reflect.DeepEqual(result, reversed) {
		t.Fatal("membership order changed result")
	}
	groups[1].DefaultNetwork = &b
	result = ResolveEffectiveConfiguration(groups)
	if !result.NetworkConflict || result.DefaultNetwork != nil || !result.NeedsAdminResolution {
		t.Fatal("conflict selected an arbitrary default")
	}
	groups[1].DefaultNetwork = nil
	groups[0].Networks = nil
	groups[1].Networks = []ResourceTemplateRef{b}
	result = ResolveEffectiveConfiguration(groups)
	if result.DefaultNetwork != nil || !result.NeedsAdminResolution {
		t.Fatal("unauthorized default accepted")
	}
}
func TestDisabledResourcesAndUnknownVolumeAccessFailClosed(t *testing.T) {
	disabled := ResourceTemplateRef{ID: "disabled"}
	enabled := ResourceTemplateRef{ID: "enabled", Enabled: true}
	got := ResolveEffectiveConfiguration([]GroupConfiguration{{ID: "group", Networks: []ResourceTemplateRef{disabled}, DefaultNetwork: &disabled, Volumes: []SharedVolumeGrant{{Volume: enabled, Access: "unknown"}, {Volume: disabled, Access: "read_write"}}}})
	if len(got.Networks) != 0 || len(got.SharedVolumes) != 0 || !got.NeedsAdminResolution {
		t.Fatalf("%+v", got)
	}
}
