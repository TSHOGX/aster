package product

import (
	"errors"
	"strings"
)

type ResourceFitIssue struct {
	Code      string            `json:"code"`
	Dimension ResourceDimension `json:"dimension,omitempty"`
	Requested int64             `json:"requested"`
	Limit     int64             `json:"limit"`
	Field     string            `json:"field,omitempty"`
}

// EnvironmentResourceFit evaluates the entire resolved mount plan, independent
// of array order. It never edits a plan to make it fit. Usage admission is a
// separate transaction concern, not a promise made by configuration preview.
func EnvironmentResourceFit(environment DefaultEnvironment, artifacts []BuiltinVolume, values map[ResourceDimension]EffectiveResource, platformMounts int64) (ResolvedEnvironment, []ResourceFitIssue, error) {
	issues := []ResourceFitIssue{}
	for _, dimension := range []ResourceDimension{ResourceCPU, ResourceMemory, ResourceOverlay, ResourceWorkspace, ResourceRuntime} {
		if values[dimension].Effective <= 0 {
			issues = append(issues, ResourceFitIssue{Code: "quota_exceeded", Dimension: dimension, Requested: 1, Limit: values[dimension].Effective})
		}
	}
	if len(issues) > 0 {
		return ResolvedEnvironment{}, issues, nil
	}
	plan, err := ResolveEnvironmentStructure(environment, artifacts, values[ResourceWorkspace].Effective, values[ResourceRuntime].Effective, platformMounts)
	if err != nil {
		var admission *AdmissionError
		if errors.As(err, &admission) {
			code, field, _ := strings.Cut(admission.Code, ":")
			return plan, []ResourceFitIssue{{Code: code, Field: field}}, nil
		}
		return plan, nil, err
	}
	for _, entry := range []struct {
		dimension ResourceDimension
		requested int64
	}{
		{ResourceVolume, plan.LargestOwnedBytes}, {ResourceProjectVolumes, plan.OwnedBytes}, {ResourceMounts, int64(len(plan.MountPlan))}, {ResourcePersistent, plan.OwnedBytes},
		{ResourceAggregateCPU, values[ResourceCPU].Effective}, {ResourceAggregateMemory, values[ResourceMemory].Effective},
	} {
		limit := values[entry.dimension].Effective
		if entry.requested > limit {
			issues = append(issues, ResourceFitIssue{Code: "quota_exceeded", Dimension: entry.dimension, Requested: entry.requested, Limit: limit})
		}
	}
	return plan, issues, nil
}
