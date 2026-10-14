package evolution

import "testing"

func TestEvaluationInheritanceAndExplicitDisabled(t *testing.T) {
	ref := Revision{"definition", 1, Digest([]byte("definition"))}
	required := EvaluationPolicy{Mode: "required", Profile: &ref, Service: &ref, ReferenceStrategy: "generation_base"}
	off := EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}
	report := required
	report.Mode = "report_only"
	for _, test := range []struct {
		name                     string
		binding, group, template *EvaluationPolicy
		mode, source             string
	}{{"default", nil, nil, nil, "disabled", "platform"}, {"template", nil, nil, &required, "required", "template"}, {"group off", nil, &off, &required, "disabled", "group"}, {"binding off", &off, &required, &required, "disabled", "binding"}, {"binding report", &report, &required, &off, "report_only", "binding"}, {"reset", nil, &required, &off, "required", "group"}} {
		t.Run(test.name, func(t *testing.T) {
			result, err := ResolveEvaluation(test.binding, test.group, test.template)
			if err != nil || result.Policy.Mode != test.mode || result.Source != test.source {
				t.Fatal(result, err)
			}
		})
	}
	invalid := EvaluationPolicy{Mode: "required"}
	if _, err := ResolveEvaluation(&invalid, &off, nil); err == nil {
		t.Fatal("invalid override silently inherited")
	}
	result, err := ResolveEvaluation(&required, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref.Revision = 2
	if result.Policy.Profile.Revision != 1 || result.Policy.Service.Revision != 1 {
		t.Fatal("resolved policy aliases mutable source")
	}
}
