package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"encoding/json"
	"fmt"
	"strings"
)

func (t Template) Validate() error {
	if err := t.Ref.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(t.Goal) == "" || len(t.Goal) > 4000 || !manifest.SupportedProfile(t.Profile) {
		return fmt.Errorf("invalid template goal or profile")
	}
	var schema map[string]any
	if json.Unmarshal(t.ParametersSchema, &schema) != nil || schema["type"] != "object" {
		return fmt.Errorf("template parameters require an object schema")
	}
	for _, ports := range []map[string]string{t.Inputs, t.Outputs} {
		if len(ports) == 0 {
			return fmt.Errorf("template ports required")
		}
		for name, digest := range ports {
			if !manifest.ValidID(name) || !manifest.ValidDigest(digest) {
				return fmt.Errorf("invalid template port")
			}
		}
	}
	if len(t.Workflows) == 0 {
		return fmt.Errorf("compatible workflows required")
	}
	seen := map[Revision]bool{}
	for _, workflow := range t.Workflows {
		if err := workflow.Validate(); err != nil {
			return err
		}
		if seen[workflow] {
			return fmt.Errorf("duplicate compatible workflow")
		}
		seen[workflow] = true
	}
	if err := manifest.ValidPath(t.SuggestedPrefix, true); err != nil {
		return err
	}
	return t.Evaluation.Validate()
}

// SupportsWorkflow requires an explicitly selected immutable revision and exact
// port schema identities. A matching goal/title never implies compatibility.
func (t Template) SupportsWorkflow(ref Revision, w Workflow, outputProfile string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	raw, err := w.Canonical()
	if err != nil {
		return err
	}
	if w.ID != ref.ID || Digest(raw) != ref.Digest {
		return fmt.Errorf("workflow revision identity mismatch")
	}
	permitted := false
	for _, allowed := range t.Workflows {
		if allowed == ref {
			permitted = true
			break
		}
	}
	if !permitted {
		return fmt.Errorf("workflow revision not selected by template")
	}
	if !equalPorts(t.Inputs, w.Inputs) || !equalPorts(t.Outputs, w.Outputs) {
		return fmt.Errorf("template workflow port schemas differ")
	}
	if outputProfile != t.Profile {
		return fmt.Errorf("output profile incompatible with template")
	}
	return nil
}
