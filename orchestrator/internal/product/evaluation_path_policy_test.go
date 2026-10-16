package product

import (
	"context"
	"errors"
	"testing"

	"aster/contracts/evolution"
)

func TestStudioPathEvaluationAuthorityKeepsGroupVolumeAndDatasetScope(t *testing.T) {
	for _, kind := range []string{"path", "root", "foreign-policy", "foreign-volume", "dataset-revoked", "denied-review"} {
		t.Run(kind, func(t *testing.T) {
			s, token, input := evaluationAuthorityFixture(t)
			ctx := context.Background()
			check := &input.Prepared.Checks[0]
			check.Path = "manual/content"
			path := check.Path
			if kind == "root" {
				path = ""
			}
			check.PolicyID = evolution.VolumePathPolicyID(input.GroupID, input.Prepared.LibraryID, path)
			check.PolicyRevision = 17 // Independent Studio history, not config revision.
			switch kind {
			case "foreign-policy":
				check.PolicyID = evolution.VolumePathPolicyID("foreign", input.Prepared.LibraryID, path)
			case "foreign-volume":
				input.Prepared.LibraryID = "foreign"
				input.Prepared.Main.LibraryID = "foreign"
				input.Prepared.Result.LibraryID = "foreign"
				input.Prepared.Sources[0].LibraryID = "foreign"
				check.PolicyID = evolution.VolumePathPolicyID(input.GroupID, "foreign", path)
				for _, action := range []string{"volume.read", "volume.review"} {
					must(t, grantFixture(s, ctx, token, "account", input.Requester.Subject, "foreign", action, "allow", 0))
				}
			case "dataset-revoked":
				_, err := s.SetEvaluationDatasetGrant(ctx, token, input.GroupID, EvaluationDatasetGrantInput{RequestID: "revoke-dataset", ExpectedRevision: 1, Artifact: input.Profile.Dataset, Service: check.Service, Enabled: false})
				must(t, err)
			case "denied-review":
				must(t, grantFixture(s, ctx, token, "account", input.Requester.Subject, input.Prepared.LibraryID, "volume.review", "deny", 1))
			}
			check.Digest, _ = check.IdentityDigest()
			input.RequirementDigest = check.Digest
			_, err := s.AuthorizeEvaluationAssignment(ctx, input)
			if kind == "path" || kind == "root" {
				must(t, err)
			} else if !errors.Is(err, ErrForbidden) {
				t.Fatal("path policy bypassed authority", err)
			}
		})
	}
}
