package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
)

// A test may replace only executable graph identity. Source, output, evaluation,
// configuration and dependency authority stay bound to the admitted source. The
// execution service verifies the requested slice against the frozen draft.
func (s *Store) checkGenerationDraftTest(ctx context.Context, q queryer, input evolution.GenerationAssignment, actor Account, checked map[string]bool) error {
	if err := authorizeEvolutionGroup(ctx, q, actor, input.GroupID, "workflow.edit"); err != nil {
		return err
	}
	if err := authorizeCorpusEvidence(ctx, q, actor, input.GroupID); err != nil {
		return err
	}
	parent, _, _, _, err := s.generationGrant(ctx, q, input.DraftTest.Source)
	if err != nil {
		return err
	}
	if parent.BindingID != input.BindingID || parent.BatchID != input.BatchID {
		return ErrForbidden
	}
	if !input.Budget.Within(parent.Budget) {
		return ErrCapacity
	}
	expected, actual := parent.Pins, input.Pins
	expected.Workflow = actual.Workflow
	expectedRaw, _ := json.Marshal(expected)
	actualRaw, _ := json.Marshal(actual)
	if !bytes.Equal(expectedRaw, actualRaw) {
		return ErrForbidden
	}
	key := "draft-test:" + input.RunID
	if checked[key] || len(checked) >= 256 {
		return ErrInvalid
	}
	checked[key] = true
	defer delete(checked, key)
	_, err = s.checkGenerationTree(ctx, q, parent, false, checked)
	return err
}
