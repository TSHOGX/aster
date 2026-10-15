package product

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func generationFixture(t *testing.T) (*Store, string, evolution.GenerationAssignment) {
	t.Helper()
	s, token, evaluation := evaluationAuthorityFixture(t)
	ctx := context.Background()
	var configRaw []byte
	must(t, s.reader.QueryRowContext(ctx, "SELECT body FROM evolution_configuration_revisions WHERE group_id=? AND revision=1", evaluation.GroupID).Scan(&configRaw))
	var config evolution.Configuration
	must(t, json.Unmarshal(configRaw, &config))
	binding := config.Bindings[0]
	artifact := evolution.ArtifactRef{ID: "corpus", GroupID: config.GroupID, SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("bytes")), Size: 5, MediaType: "application/json", GrantID: "source-grant", PolicyRevision: 1}
	_, err := s.SetGenerationInputGrant(ctx, token, config.GroupID, GenerationInputGrantInput{RequestID: "input", Artifact: artifact, SourceIDs: []string{"source"}, Enabled: true})
	must(t, err)
	pins := evolution.RunPins{ConfigurationRevision: 1, ConfigurationDigest: evolution.Digest(configRaw), SourcePolicyRevision: 1, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, Inputs: []evolution.ArtifactRef{artifact}, InputPorts: map[string]evolution.ArtifactRef{"corpus": artifact}, Base: evaluation.Prepared.Main, Output: binding.Output, Evaluation: binding.Evaluation}
	return s, token, evolution.GenerationAssignment{RunID: "run", GroupID: config.GroupID, BindingID: binding.ID, OccurrenceID: "manual", Requester: evaluation.Requester, Pins: pins, Budget: binding.Budget}
}
func TestGenerationGrantPinsScopeAndMonotonicWinningFence(t *testing.T) {
	s, _, input := generationFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	again, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	if grant != again {
		t.Fatal("grant replay changed")
	}
	different := input
	different.OccurrenceID = "other"
	if _, err = s.AuthorizeGeneration(ctx, different); !errors.Is(err, ErrConflict) {
		t.Fatal("run identity rebound", err)
	}
	scope := evolution.GenerationGrantScope{Grant: grant, Action: "execute", Fence: 0}
	_, err = s.ResolveGeneration(ctx, scope)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	if _, err = s.ResolveGeneration(ctx, scope); !errors.Is(err, ErrConflict) {
		t.Fatal("stale fence accepted", err)
	}
	read := scope
	read.Action = "read"
	if _, err = s.ResolveGeneration(ctx, read); err != nil {
		t.Fatal("metadata read raced observed fence", err)
	}
	scope.Fence = 1
	_, err = s.ResolveGeneration(ctx, scope)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 2, State: "paused"}))
	if err = s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 2, State: "running"}); !errors.Is(err, ErrConflict) {
		t.Fatal("resume reused stale fence", err)
	}
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 3, State: "running"}))
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 3, State: "completed"}))
	result := evolution.RunResultScope{GroupID: input.GroupID, RunID: input.RunID, BindingID: input.BindingID, Fence: 3, Base: input.Pins.Base, Output: input.Pins.Output, PatchDigest: evolution.Digest([]byte("patch"))}
	if err = s.AuthorizeGenerationResult(ctx, result); !errors.Is(err, ErrForbidden) {
		t.Fatal("unbound patch authorized", err)
	}
	must(t, s.BindGenerationResult(ctx, grant, result))
	must(t, s.AuthorizeGenerationResult(ctx, result))
	result.PatchDigest = evolution.Digest([]byte("different"))
	if err = s.BindGenerationResult(ctx, grant, result); !errors.Is(err, ErrConflict) {
		t.Fatal("winning patch rebound", err)
	}
	if err = s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 4, State: "running"}); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal run revived", err)
	}
}
func TestGenerationRevocationAllowsCleanupButBlocksEffects(t *testing.T) {
	s, token, input := generationFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	_, err = s.SetGenerationInputGrant(ctx, token, input.GroupID, GenerationInputGrantInput{RequestID: "revoke", ExpectedRevision: 1, Artifact: input.Pins.Inputs[0], SourceIDs: []string{"source"}, Enabled: false})
	must(t, err)
	scope := evolution.GenerationGrantScope{Grant: grant, Action: "execute", Fence: 1}
	if _, err = s.ResolveGeneration(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked input authorized work", err)
	}
	scope.Action = "cleanup"
	_, err = s.ResolveGeneration(ctx, scope)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 2, State: "cancelling"}))
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 3, State: "cancelled"}))
	settlement := GenerationSettlement{Grant: grant, Fence: 3, Tokens: 10, CostMicros: 20}
	must(t, s.SettleGeneration(ctx, settlement))
	must(t, s.SettleGeneration(ctx, settlement))
	settlement.Tokens++
	if err = s.SettleGeneration(ctx, settlement); !errors.Is(err, ErrConflict) {
		t.Fatal("settlement rewritten", err)
	}
}
func TestGenerationAdmissionRejectsChangedSourceAndBudget(t *testing.T) {
	for _, mode := range []string{"input", "ports", "budget", "config", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			s, _, input := generationFixture(t)
			switch mode {
			case "input":
				input.Pins.Inputs = append([]evolution.ArtifactRef(nil), input.Pins.Inputs...)
				input.Pins.Inputs[0].Digest = evolution.Digest([]byte("other"))
			case "ports":
				input.Pins.InputPorts = map[string]evolution.ArtifactRef{"corpus": {ID: "foreign"}}
			case "budget":
				input.Budget.MaxTokens++
			case "config":
				input.Pins.ConfigurationRevision++
			case "automatic":
				input.Automatic = true
			}
			if _, err := s.AuthorizeGeneration(context.Background(), input); err == nil {
				t.Fatal("invalid generation admitted")
			}
		})
	}
}

func TestGenerationAndEvaluationShareConcurrentCeiling(t *testing.T) {
	s, token, input := generationFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	var profile evolution.EvaluationProfile
	// Resolve the dataset profile from the same known fixture shape; it is already
	// explicitly granted by evaluationAuthorityFixture.
	ref := input.Pins.Service
	dataset := evolution.ArtifactRef{ID: "dataset", GroupID: input.GroupID, SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), Size: 4, MediaType: "application/json", GrantID: "dataset-grant", PolicyRevision: 1}
	profile = evolution.EvaluationProfile{Ref: evolution.Revision{ID: "profile", Revision: 1}, Dataset: dataset, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "accuracy", Unit: "ratio", Operator: "gte", Value: 0.9, MinimumSamples: 1}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	check := evolution.CheckRequirement{PolicyID: evolution.BindingPolicyID(input.GroupID, input.BindingID), PolicyRevision: 1, Path: input.Pins.Output.Prefix, Mode: "required", Profile: profile.Ref, Service: ref, ReferenceStrategy: "none"}
	check.Digest, _ = check.IdentityDigest()
	source, result := input.Pins.Base, input.Pins.Base
	source.CommitID = strings.Repeat("b", 64)
	result.CommitID = strings.Repeat("c", 64)
	assignment := evolution.EvaluationAssignment{JobID: "evaluation", GroupID: input.GroupID, Requester: input.Requester, Prepared: evolution.PreparedIdentity{LibraryID: input.Pins.Base.LibraryID, Main: input.Pins.Base, Sources: []evolution.ContentRef{source}, Result: result, PolicyRevision: 1, PolicyDigest: evolution.Digest([]byte("policy")), Checks: []evolution.CheckRequirement{check}}, RequirementDigest: check.Digest, Profile: profile, Budget: input.Budget}
	if _, err = s.AuthorizeEvaluationAssignment(ctx, assignment); !errors.Is(err, ErrCapacity) {
		t.Fatal("evaluation ignored generation reservation", err)
	}
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "completed"}))
	if _, err = s.AuthorizeEvaluationAssignment(ctx, assignment); !errors.Is(err, ErrCapacity) {
		t.Fatal("unsettled generation freed budget", err)
	}
	must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 1, Tokens: 10, CostMicros: 20}))
	_, err = s.AuthorizeEvaluationAssignment(ctx, assignment)
	must(t, err)
	input.RunID = "next"
	input.OccurrenceID = "next"
	if _, err = s.AuthorizeGeneration(ctx, input); !errors.Is(err, ErrCapacity) {
		t.Fatal("generation ignored evaluation reservation", err)
	}
	// Turning automatic optimization off does not revoke already admitted manual work.
	_, err = s.DisableEvolution(ctx, token, input.GroupID, DisableEvolutionInput{RequestID: "off", ExpectedRevision: 1})
	must(t, err)
	scope := evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "handoff"}
	_, err = s.ResolveGeneration(ctx, scope)
	must(t, err)
}

func TestCompletedGenerationCanSettleAfterRevocationButCannotHandoff(t *testing.T) {
	s, token, input := generationFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	_, err = s.SetGenerationInputGrant(ctx, token, input.GroupID, GenerationInputGrantInput{RequestID: "revoke-before-observation", ExpectedRevision: 1, Artifact: input.Pins.Inputs[0], SourceIDs: []string{"source"}, Enabled: false})
	must(t, err)
	// This is an authenticated report of a local terminal fact, not permission
	// to perform another effect. Accounting must not remain reserved forever.
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "completed"}))
	must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 1, Tokens: 10, CostMicros: 20}))
	if _, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "handoff"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("revocation authorized output handoff", err)
	}
}
