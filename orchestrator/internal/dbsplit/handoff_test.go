package dbsplit_test

import (
	"context"
	"testing"

	"harness-platform/orchestrator/internal/dbsplit"
	"harness-platform/orchestrator/internal/memorystore"
)

// TestMigratedDBOpensWithMemorystore is the handoff check the per-half tests
// cannot make: after the real forward migration runs on a legacy file, the
// resulting memory.db must be usable by the NEW memorystore code, with the
// migrated rows readable through its normal API.
func TestMigratedDBOpensWithMemorystore(t *testing.T) {
	ctx := context.Background()
	control, memory := newFixture(t)

	if _, err := dbsplit.Run(ctx, dbsplit.Options{
		ControlPath: control,
		MemoryPath:  memory,
		Direction:   dbsplit.Forward,
	}); err != nil {
		t.Fatalf("forward migrate: %v", err)
	}

	ms, err := memorystore.Open(ctx, memory)
	if err != nil {
		t.Fatalf("memorystore.Open on migrated file: %v", err)
	}
	defer ms.Close()

	mems, err := ms.ListSemanticMemories(ctx, "")
	if err != nil {
		t.Fatalf("ListSemanticMemories: %v", err)
	}
	if len(mems) != 5 {
		t.Fatalf("migrated semantic memories = %d, want 5", len(mems))
	}

	for _, sid := range seededSessions {
		events, err := ms.GetTrajectoryEvents(ctx, sid)
		if err != nil {
			t.Fatalf("GetTrajectoryEvents(%s): %v", sid, err)
		}
		if len(events) != 4 {
			t.Fatalf("session %s events = %d, want 4", sid, len(events))
		}
		if events[0].NativeID == "" {
			t.Fatalf("session %s lost ALTER-added native_id through migration", sid)
		}
	}

	skills, err := ms.ListSkillCandidates(ctx, "")
	if err != nil {
		t.Fatalf("ListSkillCandidates: %v", err)
	}
	if len(skills) != 5 {
		t.Fatalf("migrated skill candidates = %d, want 5", len(skills))
	}
}
