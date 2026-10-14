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
