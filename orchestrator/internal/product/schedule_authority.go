package product

import "context"

// A pause must contain only the desired paused state. Disguising a definition
// edit as a pause is not reduction authority.
func scheduleReduction(input ScheduleMutation) bool {
	switch input.Operation {
	case "cancel", "delete", "finish":
		return true
	case "update":
		patch := input.Patch
		if patch.State == nil || *patch.State != "paused" {
			return false
		}
		patch.State = nil
		return patch == (SchedulePatch{})
	}
	return false
}
func scheduleDispatchAuthority(ctx context.Context, q queryer, actor string) error {
	if err := authorizeAction(ctx, q, actor, ActionSchedulesManage); err != nil {
		return err
	}
	return authorizeAction(ctx, q, actor, ActionAgentsRun)
}
