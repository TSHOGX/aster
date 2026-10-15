package evolution

import (
	"encoding/json"
	"testing"
)

func configFixture() Configuration {
	ref := Revision{"definition", 1, Digest([]byte("definition"))}
	budget := Budget{1000, 1000, 10, 900, 1024}
	binding := func(id, prefix string) Binding {
		return Binding{ID: id, Enabled: true, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{"corpus"}, Output: OutputBinding{"library", prefix, "authored-v1", 1}, Evaluation: EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Budget: budget, Trigger: "manual"}
	}
	return Configuration{GroupID: "alpha", Revision: 1, Sources: []SourceBinding{{ID: "corpus", Kind: "volume", ResourceID: "source", PolicyRevision: 1, AudienceGroups: []string{"alpha"}, DerivativePublication: true}}, Bindings: []Binding{binding("skills", "skills"), binding("lessons", "lessons")}, Budget: budget}
}
func TestActivationConfiguration(t *testing.T) {
	if err := configFixture().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Configuration){
		"nested_output":   func(c *Configuration) { c.Bindings[1].Output.Prefix = "skills/nested" },
		"forbidden_reuse": func(c *Configuration) { c.Sources[0].DerivativePublication = false },
		"audience":        func(c *Configuration) { c.Sources[0].AudienceGroups = []string{"beta"} },
		"cycle": func(c *Configuration) {
			c.Bindings[0].DependsOn = []string{"lessons"}
			c.Bindings[1].DependsOn = []string{"skills"}
		},
		"missing_dependency":  func(c *Configuration) { c.Bindings[0].DependsOn = []string{"missing"} },
		"budget":              func(c *Configuration) { c.Bindings[0].Budget.MaxTokens++ },
		"disabled_dependency": func(c *Configuration) { c.Bindings[0].DependsOn = []string{"lessons"}; c.Bindings[1].Enabled = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := configFixture()
			mutate(&c)
			if c.Validate() == nil {
				t.Fatal("invalid activation accepted")
			}
		})
	}
}

func TestBindingCannotSelectAliasedResourceTwice(t *testing.T) {
	config := configFixture()
	alias := config.Sources[0]
	alias.ID = "alias"
	config.Sources = append(config.Sources, alias)
	config.Bindings[0].SourceIDs = append(config.Bindings[0].SourceIDs, alias.ID)
	if err := config.Validate(); err == nil {
		t.Fatal("same resource selected twice through aliases")
	}
	// Distinct policies may still be selected independently by different bindings.
	config.Bindings[0].SourceIDs = []string{"corpus"}
	config.Bindings[1].SourceIDs = []string{"alias"}
	if err := config.Validate(); err != nil {
		t.Fatal("independent binding selection rejected", err)
	}
	// Resource IDs are namespaced by source kind.
	config.Sources[1].Kind = "project"
	config.Bindings[0].SourceIDs = []string{"corpus", "alias"}
	if err := config.Validate(); err != nil {
		t.Fatal("different resource kinds conflated", err)
	}
}

func TestScheduleRequiresExplicitTimezone(t *testing.T) {
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		c := configFixture()
		c.Bindings[0].Trigger = "schedule"
		c.Bindings[0].Schedule = "0 9 * * *"
		c.Bindings[0].Timezone = zone
		if err := c.Validate(); err != nil {
			t.Fatal(zone, err)
		}
	}
	for _, zone := range []string{"", "Local", "invalid/zone", "UTC TZ=other"} {
		c := configFixture()
		c.Bindings[0].Trigger = "schedule"
		c.Bindings[0].Schedule = "0 9 * * *"
		c.Bindings[0].Timezone = zone
		if err := c.Validate(); err == nil {
			t.Fatal("invalid timezone accepted", zone)
		}
	}
	c := configFixture()
	c.Bindings[0].Timezone = "UTC"
	if c.Validate() == nil {
		t.Fatal("timezone accepted for manual trigger")
	}
}
