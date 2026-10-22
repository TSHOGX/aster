package evolution

import (
	"math"
	"testing"
)

func TestServiceCredentialReferenceValidation(t *testing.T) {
	service := Service{Ref: Revision{"service", 1, Digest([]byte("service"))}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: Digest([]byte("runner")), Capabilities: Capabilities{ProtocolVersion: 1}}
	if err := service.Validate(); err != nil {
		t.Fatal(err)
	}
	service.CredentialGeneration = 1
	if service.Validate() == nil {
		t.Fatal("generation without reference accepted")
	}
	service.CredentialRef = "registered-credential"
	if err := service.Validate(); err != nil {
		t.Fatal(err)
	}
	service.CredentialRef = "https://credential/value"
	if service.Validate() == nil {
		t.Fatal("arbitrary credential value accepted")
	}
}
func TestEvaluationThresholdDefinitionValidation(t *testing.T) {
	ref := Revision{"definition", 1, Digest([]byte("definition"))}
	profile := EvaluationProfile{Ref: ref, Dataset: ArtifactRef{ID: "dataset", GroupID: "group", SchemaDigest: Digest([]byte("schema")), Digest: Digest([]byte("bytes")), GrantID: "grant", PolicyRevision: 1, MediaType: "application/json"}, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1}}}
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		copy := profile
		copy.Thresholds = append([]MetricThreshold(nil), profile.Thresholds...)
		copy.Thresholds[0].Value = value
		if copy.Validate() == nil {
			t.Fatal("nonfinite threshold accepted")
		}
	}
	profile.Thresholds = append(profile.Thresholds, profile.Thresholds[0])
	if profile.Validate() == nil {
		t.Fatal("duplicate metric accepted")
	}
}
