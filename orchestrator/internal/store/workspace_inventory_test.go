package store

import (
	"context"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/workspace"
	"testing"
	"time"
)

func TestWorkspaceInventoryAtomicDiffAndStaleScanRefusal(t *testing.T) {
	ctx := context.Background()
	st, _, binding, _ := boundExecutionFixture(t)
	stamp := time.Now().UTC()
	one := workspace.File{Path: "one", Size: 1, ModifiedAt: stamp}
	two := workspace.File{Path: "two", Size: 2, ModifiedAt: stamp}
	first, err := st.BeginWorkspaceInventory(ctx, binding.SessionID, binding.Owner, "first")
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.CommitWorkspaceInventory(ctx, first, []workspace.File{one, two})
	if err != nil || len(events) != 2 {
		t.Fatalf("initial scan: %v %v", events, err)
	}
	old, err := st.BeginWorkspaceInventory(ctx, binding.SessionID, binding.Owner, "old")
	if err != nil {
		t.Fatal(err)
	}
	latest, err := st.BeginWorkspaceInventory(ctx, binding.SessionID, binding.Owner, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitWorkspaceInventory(ctx, old, nil); err == nil {
		t.Fatal("outdated scan removed newer metadata")
	}
	if events, err := st.CommitWorkspaceInventory(ctx, latest, []workspace.File{one}); err != nil || len(events) != 1 {
		t.Fatalf("deletion diff: %v %v", events, err)
	}
	unchanged, err := st.BeginWorkspaceInventory(ctx, binding.SessionID, binding.Owner, "unchanged")
	if err != nil {
		t.Fatal(err)
	}
	if events, err := st.CommitWorkspaceInventory(ctx, unchanged, []workspace.File{one}); err != nil || len(events) != 0 {
		t.Fatalf("unchanged scan published duplicates: %v %v", events, err)
	}
	failed, err := st.BeginWorkspaceInventory(ctx, binding.SessionID, binding.Owner, "failure")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER reject_inventory_event BEFORE INSERT ON events WHEN NEW.type='artifact.deleted' BEGIN SELECT RAISE(ABORT,'injected event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CommitWorkspaceInventory(ctx, failed, nil); err == nil {
		t.Fatal("event failure ignored")
	}
	items, err := st.ListArtifacts(ctx, binding.SessionID)
	if err != nil || len(items) != 1 || items[0].Path != "one" {
		t.Fatalf("event failure partially removed inventory: %+v %v", items, err)
	}
}

func TestWorkspaceInventoryRefusesMutationEventAndEpochRaces(t *testing.T) {
	for _, change := range []string{"write", "event", "epoch"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			st, _, binding, _ := boundExecutionFixture(t)
			scan, err := st.BeginWorkspaceInventory(ctx, binding.SessionID, binding.Owner, "scan")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "write":
				if _, err := st.db.Exec(`INSERT INTO workspace_mutations(operation_id,session_id,owner,path,state,result,error,created_at,updated_at) VALUES('write',?,?,'file','succeeded','{}','','2026-01-01','2026-01-01')`, binding.SessionID, binding.Owner); err != nil {
					t.Fatal(err)
				}
			case "event":
				if _, err := st.CommitEngineEvents(ctx, binding.StoreID, testEngineOwner(t, st), 0, []sandbox.Event{{ID: "new-event", Sequence: 1, SandboxID: binding.SandboxID, Kind: "paused"}}); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				if _, err := st.db.Exec(`UPDATE sandbox_environments SET epoch=epoch+1 WHERE environment_id=?`, binding.EnvironmentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.CommitWorkspaceInventory(ctx, scan, nil); err == nil {
				t.Fatalf("scan crossed %s", change)
			}
		})
	}
}
