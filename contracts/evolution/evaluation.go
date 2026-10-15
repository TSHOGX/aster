package evolution

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"

	manifest "aster/contracts/volumemanifest"
)

var ErrEvaluationPending = errors.New("evaluation_pending")

func (s EvaluationSubject) Validate() error {
	if err := s.Candidate.Validate(); err != nil {
		return err
	}
	if !manifest.ValidDigest(s.PreparedDigest) || !manifest.ValidDigest(s.RequirementDigest) {
		return fmt.Errorf("exact prepared and requirement identities required")
	}
	if s.Reference != nil {
		if err := s.Reference.Validate(); err != nil {
			return err
		}
	}
	if err := s.Dataset.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ref := range append([]Revision{s.Profile, s.Scorer, s.Runtime, s.Model}, s.Tools...) {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	for _, ref := range s.Tools {
		if seen[ref.ID] {
			return fmt.Errorf("duplicate evaluation tool")
		}
		seen[ref.ID] = true
	}
	return nil
}
func (s EvaluationSubject) Digest() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	// Nil and empty tool lists mean the same explicit absence of tools.
	s.Tools = append([]Revision{}, s.Tools...)
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return Digest(raw), nil
}
func (s EvaluationSubject) MatchesProfile(p EvaluationProfile) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	digest, err := DefinitionDigest(p)
	if err != nil || digest != p.Ref.Digest {
		return fmt.Errorf("profile digest mismatch")
	}
	if s.Profile != p.Ref || s.Dataset != p.Dataset || s.Scorer != p.Scorer || s.Runtime != p.Runtime || s.Model != p.Model || !reflect.DeepEqual(append([]Revision{}, s.Tools...), append([]Revision{}, p.Tools...)) {
		return fmt.Errorf("evaluation conditions differ from profile")
	}
	return nil
}

// AssessEvaluation derives the outcome from actual observations and the frozen
// profile. A provider's outcome label never substitutes for measurements.
func AssessEvaluation(profile EvaluationProfile, assigned EvaluationSubject, result EvaluationResult) (EvaluationResult, error) {
	if err := assigned.MatchesProfile(profile); err != nil {
		return result, err
	}
	expected, err := assigned.Digest()
	if err != nil {
		return result, err
	}
	actual, err := result.Subject.Digest()
	if err != nil || actual != expected {
		return result, fmt.Errorf("evaluation subject mismatch")
	}
	if !manifest.ValidID(result.JobID) || result.Fence < 0 || result.Fence == 0 && (result.State != "canceled" || len(result.Metrics) != 0 || len(result.Reports) != 0) || result.Service.Validate() != nil {
		return result, fmt.Errorf("invalid evaluation result identity")
	}
	if result.State != "completed" && result.State != "error" && result.State != "canceled" {
		return result, fmt.Errorf("nonterminal evaluation result")
	}
	if len(result.Metrics) > 1000 || len(result.Reports) > 1000 || len(result.ReproducibilityLimit) > 4000 {
		return result, fmt.Errorf("evaluation result exceeds limits")
	}
	metrics := map[string]Metric{}
	for _, metric := range result.Metrics {
		if !manifest.ValidID(metric.Name) || metric.Unit == "" || len(metric.Unit) > 128 || metric.Samples < 0 || math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) || math.IsNaN(metric.Coverage) || math.IsInf(metric.Coverage, 0) || metric.Coverage < 0 || metric.Coverage > 1 {
			return result, fmt.Errorf("invalid metric")
		}
		if _, exists := metrics[metric.Name]; exists {
			return result, fmt.Errorf("duplicate metric")
		}
		metrics[metric.Name] = metric
	}
	reports := map[string]bool{}
	for _, report := range result.Reports {
		if report.Validate() != nil || report.GroupID != assigned.Dataset.GroupID || reports[report.ID] {
			return result, fmt.Errorf("invalid scoped evaluation report")
		}
		reports[report.ID] = true
	}
	result.Outcome = "inconclusive"
	if result.State != "completed" || profile.FixedModelRequired && result.ReproducibilityLimit != "" {
		return result, nil
	}
	failed, incomplete := false, false
	for _, threshold := range profile.Thresholds {
		metric, exists := metrics[threshold.Name]
		if !exists || metric.Unit != threshold.Unit || metric.Samples < 1 || metric.Samples < threshold.MinimumSamples || metric.Coverage < threshold.MinimumCoverage {
			incomplete = true
			continue
		}
		if threshold.Operator == "gte" && metric.Value < threshold.Value || threshold.Operator == "lte" && metric.Value > threshold.Value {
			failed = true
		}
	}
	if incomplete {
		return result, nil
	}
	result.Outcome = "pass"
	if failed {
		result.Outcome = "fail"
	}
	return result, nil
}
