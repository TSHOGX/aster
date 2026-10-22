package projections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aster/contracts/evolution"
)

func generationBoundaryRun(t *testing.T, s *Store) RunLease {
	t.Helper()
	ctx := context.Background()
	body, _ := json.Marshal(evolution.Run{ID: "run", GroupID: "group", Budget: evolution.Budget{MaxTokens: 100, MaxCostMicros: 100, MaxArtifactBytes: 4096}})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body); err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestGenerationRejectsProtectedDatasetAdvertisements(t *testing.T) {
	for _, kind := range []string{"event", "checkpoint", "provider-request", "provider-output", "provider-evidence"} {
		t.Run(kind, func(t *testing.T) {
			s, _, input := evaluationAdmissionFixture(t)
			ctx := context.Background()
			raw, err := s.CatalogDefinition(ctx, "profile", input.Prepared.Checks[0].Profile)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			protected := profile.Dataset
			// Same bytes under a generation-owned identity cannot remove protection.
			protected.ID, protected.GrantID = "generation-copy", "generation-grant"
			if err = s.SealArtifact(ctx, protected, bytes.NewBufferString("data")); err != nil {
				t.Fatal(err)
			}
			lease := generationBoundaryRun(t, s)
			safe := protected
			safe.ID, safe.Digest, safe.Size = "safe", evolution.Digest([]byte("safe")), 4
			if err = s.SealArtifact(ctx, safe, bytes.NewBufferString("safe")); err != nil {
				t.Fatal(err)
			}
			if err = s.ReserveRunBudget(ctx, lease, "call", RunUsage{10, 10}); err != nil {
				t.Fatal(err)
			}
			if kind != "provider-request" {
				if _, err = s.PrepareProviderCall(ctx, lease, "call", safe); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "event":
				_, err = s.AppendRunEvent(ctx, lease, evolution.Event{ID: "event", RunID: "run", ProducerID: "worker", Fence: lease.Fence, Timestamp: time.Now(), Kind: "node-output", Artifacts: []evolution.ArtifactRef{protected}})
			case "checkpoint":
				err = s.SaveRunCheckpoint(ctx, lease, RunCheckpoint{ID: "checkpoint", RunID: "run", Fence: lease.Fence, Artifact: protected})
			case "provider-request":
				_, err = s.PrepareProviderCall(ctx, lease, "call", protected)
			case "provider-output", "provider-evidence":
				outcome := ProviderOutcome{CallID: "call", ProviderRequestID: "provider", State: "completed", Output: &safe, Usage: RunUsage{3, 4}}
				if kind == "provider-output" {
					outcome.Output = &protected
				} else {
					outcome.Evidence = []evolution.ArtifactRef{protected}
				}
				call, readErr := s.ProviderCall(ctx, lease, "call")
				if readErr != nil {
					t.Fatal(readErr)
				}
				provider := &providerFixture{outcome: outcome, committed: true, key: call.IdempotencyKey}
				for attempt := 0; attempt < 2; attempt++ {
					var result ProviderOutcome
					result, err = s.ExecuteProviderCall(ctx, lease, call, RunUsage{10, 10}, provider, &callAuthorityFixture{})
					if !errors.Is(err, ErrNotFound) || result.Output != nil || len(result.Evidence) != 0 || provider.submitted != 0 {
						t.Fatal("protected provider result escaped execution/cache", result, err, provider.submitted)
					}
				}
				if _, readErr := s.ProviderOutcome(ctx, lease, "call"); readErr != nil {
					t.Fatal("private accounting outcome unavailable", readErr)
				}
				page, pageErr := s.RunDebugger(ctx, "group", "run", "", 1)
				if pageErr != nil || len(page.Observations) != 0 || page.NextAfter != "call" || page.HasMore {
					t.Fatal("protected observation stalled debugger cursor", page, pageErr)
				}
				if _, readErr := s.RunProviderObservation(ctx, "group", "run", "call"); !errors.Is(readErr, ErrNotFound) {
					t.Fatal("protected provider observation disclosed", readErr)
				}
				if _, raw, readErr := s.RunArtifact(ctx, "group", "run", protected.ID); !errors.Is(readErr, ErrNotFound) || len(raw) != 0 {
					t.Fatal("protected provider artifact downloaded", readErr)
				}
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatal("protected dataset accepted by generation boundary", err)
			}
			budget, err := s.RunBudget(ctx, "group", "run")
			wantHeld, wantSpent := RunUsage{10, 10}, RunUsage{}
			if kind == "provider-output" || kind == "provider-evidence" {
				wantHeld, wantSpent = RunUsage{}, RunUsage{3, 4}
			}
			if err != nil || budget.Held != wantHeld || budget.Spent != wantSpent {
				t.Fatal("dataset protection corrupted provider accounting", budget, err)
			}
		})
	}
}

func TestGenerationDownloadRechecksLaterDatasetProtection(t *testing.T) {
	for _, group := range []string{"group", "foreign"} {
		t.Run(group, func(t *testing.T) {
			s, _, input := evaluationAdmissionFixture(t)
			ctx := context.Background()
			lease := generationBoundaryRun(t, s)
			secret := []byte("newly-protected-answers")
			artifact := evolution.ArtifactRef{ID: "public-first", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(secret), Size: int64(len(secret)), MediaType: "text/plain", GrantID: "grant", PolicyRevision: 1}
			if err := s.SealArtifact(ctx, artifact, bytes.NewReader(secret)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AppendRunEvent(ctx, lease, evolution.Event{ID: "event", RunID: "run", ProducerID: "worker", Fence: lease.Fence, Timestamp: time.Now(), Kind: "node-output", Artifacts: []evolution.ArtifactRef{artifact}}); err != nil {
				t.Fatal(err)
			}
			if _, body, err := s.RunArtifact(ctx, "group", "run", artifact.ID); err != nil || !bytes.Equal(body, secret) {
				t.Fatal("ordinary artifact denied", err)
			}
			for _, id := range []string{"a-safe", "z-safe"} {
				safe := artifact
				safe.ID, safe.Digest, safe.Size = id, evolution.Digest([]byte(id)), int64(len(id))
				if err := s.SealArtifact(ctx, safe, bytes.NewBufferString(id)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AppendRunEvent(ctx, lease, evolution.Event{ID: id, RunID: "run", ProducerID: "worker", Fence: lease.Fence, Timestamp: time.Now(), Kind: "node-output", Artifacts: []evolution.ArtifactRef{safe}}); err != nil {
					t.Fatal(err)
				}
			}
			protected := artifact
			protected.ID, protected.GroupID = "new-dataset", group
			if err := s.SealArtifact(ctx, protected, bytes.NewReader(secret)); err != nil {
				t.Fatal(err)
			}
			raw, err := s.CatalogDefinition(ctx, "profile", input.Prepared.Checks[0].Profile)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			profile.Ref.ID, profile.Dataset = "new-profile", protected
			profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
			raw, _ = json.Marshal(profile)
			if err := s.PublishDefinition(ctx, "profile", raw); err != nil {
				t.Fatal(err)
			}
			if _, body, err := s.RunArtifact(ctx, "group", "run", artifact.ID); !errors.Is(err, ErrNotFound) || len(body) != 0 {
				t.Fatal("held-out bytes escaped retained generation download", string(body), err)
			}
			page, err := s.RunArtifacts(ctx, "group", "run", "", 1)
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "a-safe" || !page.HasMore {
				t.Fatal("safe first page lost", page, err)
			}
			page, err = s.RunArtifacts(ctx, "group", "run", page.NextAfter, 1)
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "z-safe" || page.HasMore {
				t.Fatal("protected artifact corrupted pagination", page, err)
			}
			if body, err := s.Artifact(ctx, protected); err != nil || !bytes.Equal(body, secret) {
				t.Fatal("private evaluator artifact unavailable", err)
			}
		})
	}
}
