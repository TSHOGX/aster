package evolution

import "testing"

func TestCheckUnionPreservesIndependentRequirements(t *testing.T) {
	ref := Revision{"profile", 1, Digest([]byte("profile"))}
	required := CheckRequirement{Digest: Digest([]byte("required")), PolicyID: "skills", PolicyRevision: 1, Path: "skills", Mode: "required", Profile: ref, Service: ref, ReferenceStrategy: "none"}
	report := required
	report.Digest = Digest([]byte("report"))
	report.PolicyID = "lessons"
	report.Path = "lessons"
	report.Mode = "report_only"
	result, err := UnionChecks([]CheckRequirement{required}, nil, []CheckRequirement{report, required})
	if err != nil || len(result) != 2 {
		t.Fatal(result, err)
	}
	found := false
	for _, check := range result {
		if check.Digest == required.Digest {
			found = check.Mode == "required"
		}
	}
	if !found {
		t.Fatal("required check weakened")
	}
	weaker := required
	weaker.Mode = "report_only"
	if _, err := UnionChecks([]CheckRequirement{required}, []CheckRequirement{weaker}); err == nil {
		t.Fatal("digest reused with weaker semantics")
	}
	other := required
	other.Profile.Revision = 2
	other.Digest = Digest([]byte("different-profile"))
	result, err = UnionChecks([]CheckRequirement{required, other})
	if err != nil || len(result) != 2 {
		t.Fatal("different profiles collapsed", result, err)
	}
}
