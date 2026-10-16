package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GenerationOutput materializes one typed port from the immutable successful
// workflow checkpoint. No attempt/scratch artifact is promoted by this endpoint.
func (s *Store) GenerationOutput(ctx context.Context, scope evolution.GenerationOutputScope) (evolution.GenerationOutput, error) {
	var output evolution.GenerationOutput
	if scope.Validate() != nil {
		return output, ErrConflict
	}
	run, err := s.Run(ctx, scope.GroupID, scope.RunID)
	if err != nil {
		return output, err
	}
	if run.DraftTest != nil || run.State != "completed" || run.Fence != scope.Fence {
		return output, ErrConflict
	}
	_, grant, err := s.GenerationGrant(ctx, scope.GroupID, scope.RunID)
	if err != nil {
		return output, err
	}
	graph, err := s.Workflow(ctx, run.Pins.Workflow)
	if err != nil {
		return output, err
	}
	schema, ok := graph.Outputs[scope.Port]
	if !ok {
		return output, ErrNotFound
	}
	checkpoint, err := s.LatestRunCheckpoint(ctx, scope.GroupID, scope.RunID)
	if err != nil {
		return output, err
	}
	schemas, _ := json.Marshal(graph.Outputs)
	if checkpoint.ID != "workflow-result" || checkpoint.Artifact.GrantID != grant.ID || checkpoint.Artifact.SchemaDigest != evolution.Digest(schemas) || checkpoint.Artifact.PolicyRevision != run.Pins.SourcePolicyRevision {
		return output, ErrConflict
	}
	raw, err := s.Artifact(ctx, checkpoint.Artifact)
	if err != nil {
		return output, err
	}
	values, err := evolution.Decode[map[string]json.RawMessage](bytes.NewReader(raw))
	if err != nil {
		return output, err
	}
	body, ok := values[scope.Port]
	if !ok || int64(len(body)) > run.Budget.MaxArtifactBytes {
		return output, ErrConflict
	}
	identity := evolution.Digest([]byte(fmt.Sprintf("%s/%s/%s", scope.RunID, scope.Port, evolution.Digest(body))))
	ref := evolution.ArtifactRef{ID: "output-" + strings.TrimPrefix(identity, "sha256:"), GroupID: scope.GroupID, SchemaDigest: schema, Digest: evolution.Digest(body), Size: int64(len(body)), MediaType: "application/json", GrantID: grant.ID, PolicyRevision: run.Pins.SourcePolicyRevision}
	if err = s.SealArtifact(ctx, ref, bytes.NewReader(body)); err != nil {
		return output, err
	}
	return evolution.GenerationOutput{Scope: scope, Workflow: run.Pins.Workflow, Artifact: ref}, nil
}
