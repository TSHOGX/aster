package product

import (
	"bytes"
	"context"
	"encoding/json"

	"aster/contracts/evolution"
)

func (s *Store) checkGenerationExperiment(ctx context.Context, q queryer, input evolution.GenerationAssignment, actor Account, admission bool, checked map[string]bool) error {
	e := input.Experiment
	if err := authorizeEvolutionGroup(ctx, q, actor, input.GroupID, "workflow.edit"); err != nil {
		return err
	}
	if err := authorizeCorpusEvidence(ctx, q, actor, input.GroupID); err != nil {
		return err
	}
	parent, state, fence, _, err := s.generationGrant(ctx, q, e.Source)
	if err != nil {
		return err
	}
	if parent.BindingID != input.BindingID || parent.BatchID != input.BatchID || e.Checkpoint.PolicyRevision != parent.Pins.SourcePolicyRevision || e.Checkpoint.Size > parent.Budget.MaxArtifactBytes {
		return ErrForbidden
	}
	if admission && (state != "paused" || fence != e.SourceFence) {
		return ErrConflict
	}
	expected, actual := parent.Pins, input.Pins
	expected.Workflow = actual.Workflow
	expectedRaw, _ := json.Marshal(expected)
	actualRaw, _ := json.Marshal(actual)
	if !bytes.Equal(expectedRaw, actualRaw) {
		return ErrForbidden
	}
	// Existing immutable checkpoint lineage remains usable after the parent
	// resumes. Its source permissions are always rechecked. Bound recursion
	// also protects reads of corrupted or malicious service-supplied ancestry.
	key := "experiment:" + input.RunID
	if checked[key] || len(checked) >= 256 {
		return ErrInvalid
	}
	checked[key] = true
	defer delete(checked, key)
	_, err = s.checkGenerationTree(ctx, q, parent, false, checked)
	return err
}
