package evolution

import (
	"math"
	"strings"
	"testing"
)

func evaluationFixture(t *testing.T) (EvaluationProfile, EvaluationSubject, EvaluationResult) {
	t.Helper()
	ref := Revision{"definition", 1, Digest([]byte("definition"))}
	profile := EvaluationProfile{Ref: ref, Dataset: ArtifactRef{ID: "dataset", GroupID: "group", SchemaDigest: Digest([]byte("schema")), Digest: Digest([]byte("bytes")), GrantID: "grant", PolicyRevision: 1, MediaType: "application/json"}, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 10, MinimumCoverage: 0.8}, {Name: "latency", Unit: "ms", Operator: "lte", Value: 100, MinimumSamples: 10, MinimumCoverage: 0.8}}}
	profile.Ref.Digest, _ = DefinitionDigest(profile)
	subject := EvaluationSubject{Candidate: ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: Digest([]byte("tree"))}, PreparedDigest: Digest([]byte("prepared")), RequirementDigest: Digest([]byte("requirement")), Profile: profile.Ref, Dataset: profile.Dataset, Scorer: ref, Runtime: ref, Model: ref, Seed: 42}
	result := EvaluationResult{JobID: "job", Service: ref, Fence: 1, Subject: subject, State: "completed", Outcome: "pass", Metrics: []Metric{{Name: "accuracy", Unit: "ratio", Value: 0.95, Samples: 20, Coverage: 1}, {Name: "latency", Unit: "ms", Value: 50, Samples: 20, Coverage: 1}}}
	return profile, subject, result
}
func TestEvaluationOutcomeDerivedFromMetrics(t *testing.T) {
	for _, mode := range []string{"pass", "fail", "missing", "samples", "coverage", "unit", "error", "canceled", "nonfinite", "duplicate", "wrong-tree", "wrong-seed", "wrong-model"} {
		t.Run(mode, func(t *testing.T) {
			profile, subject, result := evaluationFixture(t)
			want := "inconclusive"
			invalid := false
			switch mode {
			case "pass":
				want = "pass"
				result.Outcome = "fail"
			case "fail":
				want = "fail"
				result.Metrics[0].Value = 0.5
			case "missing":
				result.Metrics = result.Metrics[:1]
			case "samples":
				result.Metrics[0].Samples = 9
			case "coverage":
				result.Metrics[0].Coverage = 0.79
			case "unit":
				result.Metrics[0].Unit = "percent"
			case "error", "canceled":
				result.State = mode
			case "nonfinite":
				result.Metrics[0].Value = math.NaN()
				invalid = true
			case "duplicate":
				result.Metrics = append(result.Metrics, result.Metrics[0])
				invalid = true
			case "wrong-tree":
				result.Subject.Candidate.TreeDigest = Digest([]byte("other"))
				invalid = true
			case "wrong-seed":
				result.Subject.Seed++
				invalid = true
			case "wrong-model":
				result.Subject.Model.Revision++
				invalid = true
			}
			got, err := AssessEvaluation(profile, subject, result)
			if invalid {
				if err == nil {
					t.Fatal("invalid evidence accepted")
				}
				return
			}
			if err != nil || got.Outcome != want {
				t.Fatal(got.Outcome, want, err)
			}
		})
	}
}
func TestEvaluationFixedModelAndReportScope(t *testing.T) {
	profile, subject, result := evaluationFixture(t)
	profile.FixedModelRequired = true
	profile.Ref.Digest, _ = DefinitionDigest(profile)
	subject.Profile = profile.Ref
	result.Subject = subject
	result.ReproducibilityLimit = "moving model alias"
	got, err := AssessEvaluation(profile, subject, result)
	if err != nil || got.Outcome != "inconclusive" {
		t.Fatal(got, err)
	}
	report := profile.Dataset
	report.GroupID = "foreign"
	result.Reports = []ArtifactRef{report}
	if _, err = AssessEvaluation(profile, subject, result); err == nil {
		t.Fatal("cross-group report accepted")
	}
	for _, coverage := range []float64{-1, 1.1, math.NaN(), math.Inf(1)} {
		profile.Thresholds[0].MinimumCoverage = coverage
		if profile.Validate() == nil {
			t.Fatal("invalid coverage accepted")
		}
	}
}
