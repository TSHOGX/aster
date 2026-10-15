package evolution

import (
	"encoding/json"
	"fmt"
)

// DraftSettings is authoring intent, never an executable projection. Bindings
// without inheritance flags use their explicit configuration values.
type DraftSettings struct {
	Service    *Revision                     `json:"service,omitempty"`
	Evaluation *EvaluationPolicy             `json:"evaluation,omitempty"`
	Bindings   map[string]BindingInheritance `json:"bindings"`
}

type BindingInheritance struct {
	Service    bool `json:"service"`
	Evaluation bool `json:"evaluation"`
}

// ResolveDraftSettings freezes effective values into an independent draft. The
// caller supplies policies from verified immutable template revisions, never
// browser-supplied template suggestions. Incomplete settings remain editable;
// Configuration.Validate and participant compatibility gate activation.
func ResolveDraftSettings(config Configuration, settings *DraftSettings, templates map[Revision]EvaluationPolicy) (Configuration, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return Configuration{}, err
	}
	var result Configuration
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if settings == nil {
		return result, nil
	}
	seen := map[string]bool{}
	for i := range result.Bindings {
		binding := &result.Bindings[i]
		seen[binding.ID] = true
		inherit := settings.Bindings[binding.ID]
		if inherit.Service {
			binding.Service = Revision{}
			if settings.Service != nil {
				binding.Service = *settings.Service
			}
		}
		if inherit.Evaluation {
			policy := settings.Evaluation
			if policy == nil {
				template, ok := templates[binding.Template]
				if !ok {
					return Configuration{}, fmt.Errorf("binding %s requires its pinned template evaluation policy", binding.ID)
				}
				policy = &template
			}
			// Deep copy optional profile/service/release references.
			raw, _ := json.Marshal(policy)
			binding.Evaluation = EvaluationPolicy{}
			if err = json.Unmarshal(raw, &binding.Evaluation); err != nil {
				return Configuration{}, err
			}
		}
	}
	for id := range settings.Bindings {
		if !seen[id] {
			return Configuration{}, fmt.Errorf("inheritance refers to unknown binding %s", id)
		}
	}
	return result, nil
}
