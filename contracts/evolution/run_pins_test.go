package evolution

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunPinsMatchFrozenConfigurationAndScopedInputs(t *testing.T) {
	config := configFixture()
	raw, _ := json.Marshal(config)
	binding := config.Bindings[0]
	pins := RunPins{ConfigurationRevision: config.Revision, ConfigurationDigest: Digest(raw), SourcePolicyRevision: 1, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, Model: binding.Service, Output: binding.Output, Evaluation: binding.Evaluation, Base: ContentRef{LibraryID: binding.Output.LibraryID, CommitID: strings.Repeat("a", 64), Profile: binding.Output.Profile, TreeDigest: Digest([]byte("tree"))}, Inputs: []ArtifactRef{{ID: "snapshot", GroupID: config.GroupID, SchemaDigest: Digest([]byte("schema")), Digest: Digest([]byte("snapshot")), MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}}}
	if err := pins.ValidateFor(config, binding.ID); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*RunPins){
		"configuration": func(p *RunPins) { p.ConfigurationRevision++ },
		"workflow":      func(p *RunPins) { p.Workflow.Revision++ },
		"reservation":   func(p *RunPins) { p.Output.ReservationGeneration++ },
		"base":          func(p *RunPins) { p.Base.LibraryID = "other" },
		"evaluation":    func(p *RunPins) { p.Evaluation.Mode = "report_only" },
		"inputs":        func(p *RunPins) { p.Inputs = nil },
		"cross-group":   func(p *RunPins) { p.Inputs = append([]ArtifactRef(nil), p.Inputs...); p.Inputs[0].GroupID = "other" },
		"duplicate":     func(p *RunPins) { p.Inputs = append(p.Inputs, p.Inputs[0]) },
		"model":         func(p *RunPins) { p.Model.Digest = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := pins
			mutate(&changed)
			if err := changed.ValidateFor(config, binding.ID); err == nil {
				t.Fatal("invalid run pins accepted")
			}
		})
	}
	config.Bindings[0].Enabled = false
	raw, _ = json.Marshal(config)
	pins.ConfigurationDigest = Digest(raw)
	if err := pins.ValidateFor(config, binding.ID); err == nil {
		t.Fatal("disabled binding admitted")
	}
}
