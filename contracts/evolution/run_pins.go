package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"encoding/json"
	"fmt"
)

// ValidateFor checks immutable run pins against the exact admitted configuration.
// Current central/source/artifact/reservation authority remains a separate check.
func (p RunPins) ValidateFor(config Configuration, bindingID string) error {
	if err := config.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if p.ConfigurationRevision != config.Revision || p.ConfigurationDigest != Digest(raw) {
		return fmt.Errorf("run configuration identity mismatch")
	}
	var selected *Binding
	for i := range config.Bindings {
		if config.Bindings[i].ID == bindingID {
			selected = &config.Bindings[i]
			break
		}
	}
	if selected == nil || !selected.Enabled {
		return fmt.Errorf("enabled run binding required")
	}
	if p.Template != selected.Template || p.Workflow != selected.Workflow || p.Service != selected.Service || p.Output != selected.Output {
		return fmt.Errorf("run binding pins differ from configuration")
	}
	expected, _ := json.Marshal(selected.Evaluation)
	actual, _ := json.Marshal(p.Evaluation)
	if string(expected) != string(actual) {
		return fmt.Errorf("run evaluation pins differ from configuration")
	}
	// Model and tool references belong to the immutable typed workflow closure.
	if p.SourcePolicyRevision < 1 || len(p.Inputs) == 0 {
		return fmt.Errorf("run requires source policy and sealed inputs")
	}
	inputs := map[string]bool{}
	for _, input := range p.Inputs {
		if err = input.Validate(); err != nil {
			return err
		}
		if input.GroupID != config.GroupID || inputs[input.ID] {
			return fmt.Errorf("invalid run input group or duplicate artifact")
		}
		inputs[input.ID] = true
	}
	if len(selected.Inputs) > 0 {
		if len(selected.Inputs) != len(p.InputPorts) {
			return fmt.Errorf("run input ports differ from binding")
		}
		for name, spec := range selected.Inputs {
			ref, ok := p.InputPorts[name]
			if !ok || spec.Artifact != nil && ref != *spec.Artifact || spec.Artifact == nil && ref.SchemaDigest != Digest(CorpusSchema) {
				return fmt.Errorf("run input differs from binding")
			}
		}
	}
	for name, artifact := range p.InputPorts {
		if !manifest.ValidID(name) {
			return fmt.Errorf("invalid workflow input port")
		}
		matched := false
		for _, input := range p.Inputs {
			if input == artifact {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("workflow port references undeclared input")
		}
	}
	if err = p.Base.Validate(); err != nil {
		return err
	}
	if p.Base.LibraryID != p.Output.LibraryID || p.Base.Profile != p.Output.Profile {
		return fmt.Errorf("run base differs from output library")
	}
	return nil
}
