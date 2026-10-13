package product

import (
	"context"
)

// Each update kind owns compatibility, target resolution and effective-snapshot
// projection. Lifecycle owns effects and observation, not the admin HTTP handler.
// A runtime strategy must declare a different effect and implement its own
// executor; it must never silently enter the paused data-binding patch path.
type environmentUpdateStrategy struct {
	Kind       string
	Effect     string
	Boundary   string
	Rollback   bool
	Targets    func(DefaultEnvironment) []EnvironmentMount
	Compatible func(context.Context, queryer, DefaultEnvironment, []EnvironmentMount) (bool, error)
	Resolve    func(context.Context, queryer, EnvironmentMount, string) (VolumePlan, error)
	Apply      func(DefaultEnvironment, []EnvironmentMount) DefaultEnvironment
}

const publicationReplaceKind = "publication_replace"

func updateStrategy(kind string) (environmentUpdateStrategy, error) {
	switch kind {
	case publicationReplaceKind:
		return environmentUpdateStrategy{
			Kind: publicationReplaceKind, Effect: "paused_binding", Boundary: "activation", Rollback: true,
			Targets: publicationMounts, Compatible: publicationCompatible, Resolve: publicationPlan, Apply: replacePublications,
		}, nil
	default:
		return environmentUpdateStrategy{}, Admission("update_kind_unsupported", ErrInvalid)
	}
}
