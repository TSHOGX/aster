package product

import (
	"aster/contracts/evolution"
	"context"
	"errors"
	"strings"
	"testing"
)

func draftTestAuthorityFixture(t *testing.T) (*Store, string, evolution.GenerationAssignment, evolution.GenerationAssignment) {
	t.Helper()
	s, token, source, experiment := experimentAuthorityFixture(t)
	test := experiment
	test.Experiment = nil
	test.DraftTest = &evolution.GenerationDraftTest{Source: experiment.Experiment.Source, Draft: test.Pins.Workflow, Kind: "workflow"}
	test.RunID, test.OccurrenceID = "draft-test", "draft-test"
	return s, token, source, test
}

func TestDraftTestAuthorityUsesSharedBudgetWithoutProductionEffects(t *testing.T) {
	s, _, source, input := draftTestAuthorityFixture(t)
	ctx := context.Background()
	grant, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	replay, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	if replay != grant {
		t.Fatal("test lost-reply recovery changed identity")
	}
	ordinary := source
	ordinary.RunID, ordinary.OccurrenceID = "ordinary", "ordinary"
	if _, err = s.AuthorizeGeneration(ctx, ordinary); !errors.Is(err, ErrCapacity) {
		t.Fatal("test changed ordinary binding concurrency", err)
	}
	second := input
	second.RunID, second.OccurrenceID = "second-test", "second-test"
	_, err = s.AuthorizeGeneration(ctx, second)
	must(t, err)
	third := input
	third.RunID, third.OccurrenceID = "third-test", "third-test"
	if _, err = s.AuthorizeGeneration(ctx, third); !errors.Is(err, ErrCapacity) {
		t.Fatal("test bypassed group capacity", err)
	}
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	_, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "execute"})
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "completed"}))
	if _, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "handoff"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("test authorized handoff", err)
	}
	scope := evolution.RunResultScope{GroupID: input.GroupID, RunID: input.RunID, BindingID: input.BindingID, Fence: 1, Base: input.Pins.Base, Output: input.Pins.Output, PatchDigest: evolution.Digest([]byte("test-patch"))}
	if err = s.BindGenerationResult(ctx, grant, scope); err == nil {
		t.Fatal("test bound a production patch")
	}
	if err = s.AuthorizeGenerationResult(ctx, scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("test authorized production result", err)
	}
	must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 1, Tokens: 1, CostMicros: 1}))
	// Settlement releases only the test hold; another bounded test now fits.
	_, err = s.AuthorizeGeneration(ctx, third)
	must(t, err)
	var state string
	must(t, s.reader.QueryRowContext(ctx, "SELECT state FROM generation_authorizations WHERE run_id=?", source.RunID).Scan(&state))
	if state != "paused" {
		t.Fatal("test mutated source run", state)
	}
}

func TestDraftTestRejectsReboundPinsAndRechecksSourcePermissions(t *testing.T) {
	s, token, _, input := draftTestAuthorityFixture(t)
	ctx := context.Background()
	for _, name := range []string{"automatic", "experiment", "kind", "node", "full-draft", "source-group", "source-grant", "self", "batch", "base", "budget", "evaluation"} {
		t.Run(name, func(t *testing.T) {
			changed := input
			lineage := *input.DraftTest
			changed.DraftTest = &lineage
			switch name {
			case "automatic":
				changed.Automatic = true
			case "experiment":
				changed.Experiment = &evolution.GenerationExperiment{}
			case "kind":
				lineage.Kind = "unknown"
			case "node":
				lineage.Kind = "node"
			case "full-draft":
				lineage.Draft.ID = "other"
			case "source-group":
				lineage.Source.GroupID = "foreign"
			case "source-grant":
				lineage.Source.AssignmentDigest = evolution.Digest([]byte("forged"))
			case "self":
				lineage.Source.RunID = changed.RunID
			case "batch":
				changed.BatchID = "other"
			case "base":
				changed.Pins.Base.CommitID = strings.Repeat("b", 64)
			case "budget":
				changed.Budget.MaxTokens++
			case "evaluation":
				changed.Pins.Evaluation.Mode = "off"
			}
			if _, err := s.AuthorizeGeneration(ctx, changed); err == nil {
				t.Fatal("invalid test admitted")
			}
		})
	}
	grant, err := s.AuthorizeGeneration(ctx, input)
	must(t, err)
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
	for _, action := range []string{"workflow.edit", "source.read", "run.start"} {
		revision := int64(1)
		if action == "run.start" {
			revision = 0
		}
		_, err = s.SetEvolutionGroupGrant(ctx, token, input.GroupID, EvolutionGroupGrantInput{RequestID: "deny-" + strings.ReplaceAll(action, ".", "-"), AccountID: input.Requester.Subject, Action: action, Effect: "deny", ExpectedRevision: revision})
		must(t, err)
		if _, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "execute"}); !errors.Is(err, ErrForbidden) {
			t.Fatal("test effect after revocation", action, err)
		}
		if _, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "cleanup"}); err != nil {
			t.Fatal("test cleanup blocked", action, err)
		}
		_, err = s.SetEvolutionGroupGrant(ctx, token, input.GroupID, EvolutionGroupGrantInput{RequestID: "restore-" + strings.ReplaceAll(action, ".", "-"), AccountID: input.Requester.Subject, Action: action, Effect: "allow", ExpectedRevision: revision + 1})
		must(t, err)
	}
	_, err = s.SetGenerationInputGrant(ctx, token, input.GroupID, GenerationInputGrantInput{RequestID: "deny-input", ExpectedRevision: 1, Artifact: input.Pins.Inputs[0], SourceIDs: []string{"source"}, Enabled: false})
	must(t, err)
	if _, err = s.ResolveGeneration(ctx, evolution.GenerationGrantScope{Grant: grant, Fence: 1, Action: "execute"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("test bypassed original source revocation", err)
	}
}

func TestDraftTestScopesUseTerminalSourceWithoutCheckpointPause(t *testing.T) {
	s, _, _, input := draftTestAuthorityFixture(t)
	ctx := context.Background()
	source := input.DraftTest.Source
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: source, Fence: 3, State: "running"}))
	must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: source, Fence: 3, State: "completed"}))
	must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: source, Fence: 3}))
	for _, kind := range []string{"node", "subgraph"} {
		scoped := input
		scope := *input.DraftTest
		scoped.DraftTest = &scope
		scoped.RunID, scoped.OccurrenceID = "test-"+kind, "test-"+kind
		scope.Kind, scope.NodeID = kind, "selected"
		scoped.Pins.Workflow = evolution.Revision{ID: "slice-" + kind, Revision: 1, Digest: evolution.Digest([]byte(kind))}
		grant, err := s.AuthorizeGeneration(ctx, scoped)
		must(t, err)
		scope.NodeID = "different"
		if _, err = s.AuthorizeGeneration(ctx, scoped); !errors.Is(err, ErrConflict) {
			t.Fatal("test scope changed after admission", kind, err)
		}
		if grant.AssignmentDigest == "" {
			t.Fatal("test scope not frozen")
		}
	}
}
