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
