package evolution

// ResolvedEvaluation records which setting supplied the immutable effective
// policy. Nil means inheritance; an explicit disabled policy is never omission.
// Volume/path obligations are composed separately and cannot be relaxed here.
type ResolvedEvaluation struct {
	Policy EvaluationPolicy `json:"policy"`
	Source string           `json:"source"`
}

func ResolveEvaluation(binding, group, template *EvaluationPolicy) (ResolvedEvaluation, error) {
	result := ResolvedEvaluation{Policy: EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Source: "platform"}
	for _, candidate := range []struct {
		policy *EvaluationPolicy
		source string
	}{{binding, "binding"}, {group, "group"}, {template, "template"}} {
		if candidate.policy == nil {
			continue
		}
		if err := candidate.policy.Validate(); err != nil {
			return ResolvedEvaluation{}, err
		}
		result.Policy = *candidate.policy
		result.Source = candidate.source
		// A resolved projection must not alias mutable configuration editor state.
		if result.Policy.Profile != nil {
			value := *result.Policy.Profile
			result.Policy.Profile = &value
		}
		if result.Policy.Service != nil {
			value := *result.Policy.Service
			result.Policy.Service = &value
		}
		if result.Policy.ReferenceRelease != nil {
			value := *result.Policy.ReferenceRelease
			result.Policy.ReferenceRelease = &value
		}
		break
	}
	return result, nil
}
