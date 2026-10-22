package product

import (
	"context"
	"errors"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

func experimentAuthorityFixture(t *testing.T) (*Store, string, evolution.GenerationAssignment, evolution.GenerationAssignment) {
	t.Helper()
	s, token, source := generationFixture(t)
	ctx := context.Background()
	for _, action := range []string{"workflow.edit", "source.read"} {
		_, err := s.SetEvolutionGroupGrant(ctx, token, source.GroupID, EvolutionGroupGrantInput{RequestID: strings.ReplaceAll(action, ".", "-"), AccountID: source.Requester.Subject, Action: action, Effect: "allow"})
		must(t, err)
	}
	source.Budget.MaxTokens /= 3
	source.Budget.MaxCostMicros /= 3
	grant, err := s.AuthorizeGeneration(ctx, source)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 2, State: "paused"}))
	experiment := source
	experiment.RunID, experiment.OccurrenceID = "experiment", "experiment-occurrence"
	experiment.Pins.Workflow = evolution.Revision{ID: "edited", Revision: 1, Digest: evolution.Digest([]byte("edited workflow"))}
	experiment.Experiment = &evolution.GenerationExperiment{Source: grant, SourceFence: 2, CheckpointID: "node-agent", CheckpointFence: 1, StartNode: "downstream", Checkpoint: evolution.ArtifactRef{ID: "checkpoint", GroupID: source.GroupID, SchemaDigest: evolution.Digest([]byte("checkpoint schema")), Digest: evolution.Digest([]byte("checkpoint")), Size: 10, MediaType: "application/json", GrantID: grant.ID, PolicyRevision: source.Pins.SourcePolicyRevision}}
	return s, token, source, experiment
}

func TestGenerationExperimentReservesAlongsidePausedSourceAndRetainsLineage(t *testing.T) {
	s, token, source, experiment := experimentAuthorityFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeGeneration(ctx, experiment)
	must(t, err)
	// A separate ordinary run is still excluded by the paused ordinary source.
	ordinary := source
	ordinary.RunID, ordinary.OccurrenceID = "ordinary", "ordinary"
	if _, err = s.AuthorizeGeneration(ctx, ordinary); !errors.Is(err, ErrCapacity) {
		t.Fatal("ordinary concurrency rule lost", err)
	}
	var heldTokens, heldCost int64
	must(t, s.reader.QueryRowContext(ctx, "SELECT sum(reserved_tokens),sum(reserved_cost) FROM generation_authorizations WHERE settlement IS NULL").Scan(&heldTokens, &heldCost))
	if heldTokens != 2*source.Budget.MaxTokens || heldCost != 2*source.Budget.MaxCostMicros {
		t.Fatal("experiment released source hold", heldTokens, heldCost)
	}
	tooLarge := experiment
	tooLarge.RunID, tooLarge.OccurrenceID = "too-large", "too-large"
	tooLarge.Budget.MaxTokens *= 3
	tooLarge.Budget.MaxCostMicros *= 3
	if _, err = s.AuthorizeGeneration(ctx, tooLarge); !errors.Is(err, ErrCapacity) {
		t.Fatal("experiment bypassed shared budget", err)
	}
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: experiment.Experiment.Source, Fence: 3, State: "running"}))
	// A committed request can recover its exact receipt after source resume;
	// new requests cannot claim that the old pause is still current.
	replay, err := s.AuthorizeGeneration(ctx, experiment)
	must(t, err)
	if replay != grant {
		t.Fatal("lost reply changed experiment grant")
	}
	late := experiment
	late.RunID, late.OccurrenceID = "late", "late"
	if _, err = s.AuthorizeGeneration(ctx, late); !errors.Is(err, ErrConflict) {
		t.Fatal("stale pause admitted new experiment", err)
	}
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	_, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "execute"})
	must(t, err)
	_, err = s.SetGenerationInputGrant(ctx, token, source.GroupID, GenerationInputGrantInput{RequestID: "revoke-source", ExpectedRevision: 1, Artifact: source.Pins.Inputs[0], SourceIDs: []string{"source"}, Enabled: false})
	must(t, err)
	if _, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "execute"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("checkpoint reuse bypassed source revocation", err)
	}
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 2, State: "cancelled"}))
	must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 2}))
	var sourceState string
	must(t, s.reader.QueryRowContext(ctx, "SELECT state FROM generation_authorizations WHERE run_id=?", source.RunID).Scan(&sourceState))
	if sourceState != "running" {
		t.Fatal("experiment cancellation changed source", sourceState)
	}
}

func TestGenerationExperimentRejectsReboundLineageAndMissingPermissions(t *testing.T) {
	s, token, _, input := experimentAuthorityFixture(t)
	ctx := context.Background()
	for _, name := range []string{"checkpoint-grant", "checkpoint-group", "checkpoint-fence", "checkpoint-policy", "source-fence", "source-grant", "base", "batch", "automatic", "start-node", "workflow"} {
		t.Run(name, func(t *testing.T) {
			changed := input
			lineage := *input.Experiment
			changed.Experiment = &lineage
			switch name {
			case "checkpoint-grant":
				lineage.Checkpoint.GrantID = "foreign"
			case "checkpoint-group":
				lineage.Checkpoint.GroupID = "foreign"
			case "checkpoint-fence":
				lineage.CheckpointFence = lineage.SourceFence
			case "checkpoint-policy":
				lineage.Checkpoint.PolicyRevision++
			case "source-fence":
				lineage.SourceFence++
			case "source-grant":
				lineage.Source.AssignmentDigest = evolution.Digest([]byte("forged"))
			case "base":
				changed.Pins.Base.CommitID = strings.Repeat("b", 64)
			case "batch":
				changed.BatchID = "foreign"
			case "automatic":
				changed.Automatic = true
			case "start-node":
				lineage.StartNode = ""
			case "workflow":
				changed.Pins.Workflow.Digest = "invalid"
			}
			if _, err := s.AuthorizeGeneration(ctx, changed); err == nil {
				t.Fatal("rebound experiment admitted")
			}
		})
	}
	for _, action := range []string{"workflow.edit", "source.read", "run.start"} {
		revision := int64(1)
		if action == "run.start" {
			revision = 0
		}
		_, err := s.SetEvolutionGroupGrant(ctx, token, input.GroupID, EvolutionGroupGrantInput{RequestID: "deny-" + strings.ReplaceAll(action, ".", "-"), AccountID: input.Requester.Subject, Action: action, Effect: "deny", ExpectedRevision: revision})
		must(t, err)
		if _, err = s.AuthorizeGeneration(ctx, input); !errors.Is(err, ErrForbidden) {
			t.Fatal("experiment missing permission", action, err)
		}
		_, err = s.SetEvolutionGroupGrant(ctx, token, input.GroupID, EvolutionGroupGrantInput{RequestID: "restore-" + strings.ReplaceAll(action, ".", "-"), AccountID: input.Requester.Subject, Action: action, Effect: "allow", ExpectedRevision: revision + 1})
		must(t, err)
	}
}
