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
		c.Bindings[0].Inputs = map[string]BindingInput{"corpus": {Artifact: &ArtifactRef{ID: "corpus", GroupID: c.GroupID, SchemaDigest: Digest(nil), Digest: Digest(nil), Size: 0, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}}}
		c.Bindings[0].Schedule = "0 9 * * *"
		c.Bindings[0].Timezone = zone
		if err := c.Validate(); err != nil {
			t.Fatal(zone, err)
		}
	}
	for _, zone := range []string{"", "Local", "invalid/zone", "UTC TZ=other"} {
		c := configFixture()
		c.Bindings[0].Trigger = "schedule"
		c.Bindings[0].Inputs = map[string]BindingInput{"corpus": {Artifact: &ArtifactRef{ID: "corpus", GroupID: c.GroupID, SchemaDigest: Digest(nil), Digest: Digest(nil), Size: 0, MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}}}
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

func TestAutomaticInputDeclarationsAndWorkflowPortSchemas(t *testing.T) {
	c := configFixture()
	c.Sources[0].Kind = "project"
	b := &c.Bindings[0]
	b.Trigger = "event"
	if c.Validate() == nil {
		t.Fatal("automatic binding lacks explicit ports")
	}
	b.Inputs = map[string]BindingInput{"evidence": {Sources: []string{"corpus"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	workflow := Workflow{Inputs: map[string]string{"evidence": Digest(CorpusSchema)}}
	if err := b.ValidateInputPorts(workflow); err != nil {
		t.Fatal(err)
	}
	workflow.Inputs["evidence"] = Digest([]byte("other schema"))
	if b.ValidateInputPorts(workflow) == nil {
		t.Fatal("wrong corpus schema accepted")
	}
	b.Inputs["evidence"] = BindingInput{Sources: []string{"corpus", "corpus"}}
	if c.Validate() == nil {
		t.Fatal("duplicate source accepted")
	}
	b.Inputs["evidence"] = BindingInput{Sources: []string{"unbound"}}
	if c.Validate() == nil {
		t.Fatal("unbound source accepted")
	}
}

func TestTypedBindingDependencyDeclarations(t *testing.T) {
	c := configFixture()
	parent := c.Bindings[0]
	child := &c.Bindings[1]
	child.DependsOn = []string{parent.ID}
	child.Inputs = map[string]BindingInput{"upstream": {Dependency: &BindingDependency{BindingID: parent.ID, OutputPort: "candidate"}}}
	if err := c.Validate(); err != nil {
		t.Fatal("valid typed dependency rejected", err)
	}
	child.DependsOn = nil
	if c.Validate() == nil {
		t.Fatal("undeclared dependency input accepted")
	}
	child.DependsOn = []string{parent.ID}
	child.Inputs = nil
	if c.Validate() == nil {
		t.Fatal("untyped dependency accepted")
	}
	child.Inputs = map[string]BindingInput{"upstream": {Sources: []string{"corpus"}, Dependency: &BindingDependency{BindingID: parent.ID, OutputPort: "candidate"}}}
	if c.Validate() == nil {
		t.Fatal("ambiguous dependency input accepted")
	}
	child.Inputs = map[string]BindingInput{"upstream": {Dependency: &BindingDependency{BindingID: parent.ID, OutputPort: "candidate"}}}
	alias := c.Sources[0]
	alias.ID = "other"
	alias.ResourceID = "other-resource"
	c.Sources = append(c.Sources, alias)
	child.SourceIDs = []string{"other"}
	if c.Validate() == nil {
		t.Fatal("dependency silently inherited undeclared sources")
	}
}

func TestSourceExclusionsUseExplicitEvidenceSelectors(t *testing.T) {
	c := configFixture()
	c.Sources[0].Kind = "project"
	for _, selectors := range [][]string{{"sessions/private"}, {"turns/task", "executions/attempt"}} {
		c.Sources[0].Exclusions = selectors
		if err := c.Validate(); err != nil {
			t.Fatal(selectors, err)
		}
	}
	for _, selectors := range [][]string{{"private"}, {"sessions/*"}, {"turns/../private"}, {"unknown/id"}, {"turns/task", "turns/task"}} {
		c.Sources[0].Exclusions = selectors
		if c.Validate() == nil {
			t.Fatal("unsupported selector accepted", selectors)
		}
	}
}

func TestBudgetWithinRequiresEveryFiniteDimension(t *testing.T) {
	ceiling := Budget{MaxTokens: 100, MaxCostMicros: 100, MaxAttempts: 10, TimeoutSeconds: 60, MaxArtifactBytes: 1024}
	reduced := Budget{MaxTokens: 10, MaxCostMicros: 0, MaxAttempts: 1, TimeoutSeconds: 30, MaxArtifactBytes: 512}
	if !reduced.Within(ceiling) || !ceiling.Within(ceiling) {
		t.Fatal("valid tightened budget rejected")
	}
	for _, change := range []func(*Budget){func(b *Budget) { b.MaxTokens = 101 }, func(b *Budget) { b.MaxCostMicros = 101 }, func(b *Budget) { b.MaxAttempts = 11 }, func(b *Budget) { b.TimeoutSeconds = 61 }, func(b *Budget) { b.MaxArtifactBytes = 1025 }, func(b *Budget) { b.MaxTokens = 0 }} {
		budget := reduced
		change(&budget)
		if budget.Within(ceiling) {
			t.Fatal("invalid bound accepted", budget)
		}
	}
}
