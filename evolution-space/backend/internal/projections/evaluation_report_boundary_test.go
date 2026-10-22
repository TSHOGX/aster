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

func TestEvaluationReportCannotAliasAnotherHeldOutDataset(t *testing.T) {
	for _, kind := range []string{"same-id", "same-bytes", "foreign-dataset", "protected-after-completion"} {
		t.Run(kind, func(t *testing.T) {
			s, _, input := evaluationAdmissionFixture(t)
			ctx := context.Background()
			job, err := s.AdmitEvaluation(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := s.ClaimEvaluation(ctx, "group", "job", "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			submission, err := s.PrepareEvaluationDispatch(ctx, lease, "grant")
			if err != nil {
				t.Fatal(err)
			}
			secret := []byte(`{"answers":"private-other-evaluation"}`)
			dataset := job.Profile.Dataset
			dataset.ID, dataset.Digest, dataset.Size = "other-dataset", evolution.Digest(secret), int64(len(secret))
			if kind == "foreign-dataset" {
				dataset.GroupID = "other-group"
			}
			if err = s.SealArtifact(ctx, dataset, bytes.NewReader(secret)); err != nil {
				t.Fatal(err)
			}
			report := dataset
			report.GroupID = job.GroupID
			if kind != "same-id" {
				report.ID = "report-copy"
			}
			if err = s.SealArtifact(ctx, report, bytes.NewReader(secret)); err != nil {
				t.Fatal(err)
			}
			protect := func() {
				profile := job.Profile
				profile.Ref.ID = "other-profile"
				profile.Dataset = dataset
				profile.Ref.Digest, err = evolution.DefinitionDigest(profile)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(profile)
				if err = s.PublishDefinition(ctx, "profile", raw); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "protected-after-completion" {
				protect()
			}
			completion := evaluationCompletion(job, submission)
			completion.Result.Reports = []evolution.ArtifactRef{report}
			_, err = s.CompleteEvaluation(ctx, lease, completion)
			if kind != "protected-after-completion" {
				if err == nil {
					t.Fatal("another held-out dataset published as report")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, raw, err := s.EvaluationReportArtifact(ctx, "group", "job", report.ID); err != nil || !bytes.Equal(raw, secret) {
				t.Fatal("unrestricted report", err)
			}
			protect()
			if _, raw, err := s.EvaluationReportArtifact(ctx, "group", "job", report.ID); !errors.Is(err, ErrNotFound) || len(raw) != 0 {
				t.Fatal("newly protected dataset still downloadable", err)
			}
		})
	}
}
