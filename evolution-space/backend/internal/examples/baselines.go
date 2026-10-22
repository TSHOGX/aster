package examples

import (
	"fmt"

	"aster/contracts/evolution"
)

// Baseline is a starter evaluation profile shape for one memory class. Metric
// names are the contract a group's scorer must report; Aster compares them with
// the frozen thresholds and never accepts a scorer's own pass/fail label.
type Baseline struct {
	Template          string                      `json:"template"`
	Description       string                      `json:"description"`
	Dataset           string                      `json:"dataset"`
	DatasetExample    string                      `json:"dataset_example"`
	ReferenceStrategy string                      `json:"reference_strategy"`
	Thresholds        []evolution.MetricThreshold `json:"thresholds"`
}

func threshold(name, unit, operator string, value float64, samples int64, coverage float64) evolution.MetricThreshold {
	return evolution.MetricThreshold{Name: name, Unit: unit, Operator: operator, Value: value, MinimumSamples: samples, MinimumCoverage: coverage}
}

// Every baseline pairs an absolute floor with a non-regression delta against the
// reference content, so a candidate cannot pass by being good enough while still
// making the accepted knowledge worse. Values are conservative starting points
// for a small held-out set; groups publish their own revisions as data grows.
var baselines = []Baseline{
	{Template: "example-skills", ReferenceStrategy: "target_main",
		DatasetExample: "{\"task\":\"Deploy billing-api to staging\",\"check\":\"GET /healthz returns 200 after the deploy\"}",
		Description:    "Representative held-out tasks run with the candidate skills mounted.",
		Dataset:        "Held-out tasks with an observable completion check per task.",
		Thresholds: []evolution.MetricThreshold{
			threshold("task_success_rate", "ratio", "gte", 0.8, 20, 0.9),
			threshold("task_success_delta", "ratio", "gte", 0, 20, 0.9),
			threshold("mean_tokens_per_task", "tokens", "lte", 20000, 20, 0.9),
		}},
	{Template: "example-corrections", ReferenceStrategy: "target_main",
		DatasetExample: "{\"task\":\"Run the orders migration\",\"previous_error\":\"Migration ran without a backup\",\"expected\":\"A backup is taken and verified first\"}",
		Description:    "Replays tasks that previously failed and checks the same mistake does not recur.",
		Dataset:        "Previously failed tasks with the expected corrected result; keep them out of the generation corpus.",
		Thresholds: []evolution.MetricThreshold{
			threshold("repeat_error_rate", "ratio", "lte", 0.1, 10, 0.9),
			threshold("task_success_delta", "ratio", "gte", 0, 10, 0.9),
		}},
	{Template: "example-facts", ReferenceStrategy: "target_main",
		DatasetExample: "{\"question\":\"Where is the billing-api health check?\",\"answer\":\"GET /healthz\",\"as_of\":\"2026-10-01\"}",
		Description:    "Closed-book questions answered from the candidate facts only.",
		Dataset:        "Questions with reference answers, including time-sensitive and updated facts.",
		Thresholds: []evolution.MetricThreshold{
			threshold("answer_accuracy", "ratio", "gte", 0.85, 30, 0.9),
			threshold("contradiction_rate", "ratio", "lte", 0.02, 30, 0.9),
			threshold("answer_accuracy_delta", "ratio", "gte", 0, 30, 0.9),
		}},
	{Template: "example-knowledge", ReferenceStrategy: "target_main",
		DatasetExample: "{\"question\":\"How many active users were there yesterday?\",\"expected_definition\":\"active-users\",\"answer\":\"count(distinct user_id) for yesterday\"}",
		Description:    "Business questions answered against the semantic layer, graded on the definition used and the answer.",
		Dataset:        "Business questions with the expected metric, dimension or entity and the reference answer.",
		Thresholds: []evolution.MetricThreshold{
			threshold("answer_accuracy", "ratio", "gte", 0.8, 30, 0.9),
			threshold("definition_coverage", "ratio", "gte", 0.8, 30, 0.9),
			threshold("answer_accuracy_delta", "ratio", "gte", 0, 30, 0.9),
		}},
	{Template: "example-episodes", ReferenceStrategy: "target_main",
		DatasetExample: "{\"query\":\"A deploy migration failed halfway\",\"relevant_episode_ids\":[\"deploy-0412\"]}",
		Description:    "Retrieves relevant past cases for new situations and checks records stay faithful to their source.",
		Dataset:        "Queries labeled with the relevant source episodes, plus records sampled for faithfulness grading.",
		Thresholds: []evolution.MetricThreshold{
			threshold("recall_at_5", "ratio", "gte", 0.7, 20, 0.9),
			threshold("faithfulness", "ratio", "gte", 0.9, 20, 0.9),
		}},
}

// Baselines returns copies so callers cannot alter the shared presets.
func Baselines() []Baseline {
	result := make([]Baseline, len(baselines))
	for i, b := range baselines {
		b.Thresholds = append([]evolution.MetricThreshold{}, b.Thresholds...)
		result[i] = b
	}
	return result
}
func BaselineFor(template string) (Baseline, bool) {
	for _, b := range Baselines() {
		if b.Template == template {
			return b, true
		}
	}
	return Baseline{}, false
}

// BaselineInput binds a preset to a group's own sealed dataset and registered
// scorer, runtime and model. Nothing here grants dataset or evaluator use.
type BaselineInput struct {
	Template           string                `json:"template"`
	Ref                evolution.Revision    `json:"ref"`
	Dataset            evolution.ArtifactRef `json:"dataset"`
	Scorer             evolution.Revision    `json:"scorer"`
	Runtime            evolution.Revision    `json:"runtime"`
	Model              evolution.Revision    `json:"model"`
	Tools              []evolution.Revision  `json:"tools"`
	FixedModelRequired bool                  `json:"fixed_model_required"`
}

func BaselineProfile(input BaselineInput) (evolution.EvaluationProfile, error) {
	preset, ok := BaselineFor(input.Template)
	if !ok {
		return evolution.EvaluationProfile{}, fmt.Errorf("unknown starter template")
	}
	tools := append([]evolution.Revision{}, input.Tools...)
	profile := evolution.EvaluationProfile{Ref: evolution.Revision{ID: input.Ref.ID, Revision: input.Ref.Revision}, Dataset: input.Dataset, Scorer: input.Scorer, Runtime: input.Runtime, Model: input.Model, Tools: tools, Thresholds: preset.Thresholds, FixedModelRequired: input.FixedModelRequired}
	digest, err := evolution.DefinitionDigest(profile)
	if err != nil {
		return profile, err
	}
	profile.Ref.Digest = digest
	return profile, profile.Validate()
}

// Starter is the browser description of one starter template and its baseline.
type Starter struct {
	Class
	Baseline Baseline `json:"baseline"`
}

func Starters() []Starter {
	result := []Starter{}
	for _, c := range Classes() {
		b, _ := BaselineFor(c.Template)
		result = append(result, Starter{c, b})
	}
	return result
}
