package khora

import (
	v1 "github.com/TSHOGX/khora/pkg/transport/gen/khora/transport/v1"
	"testing"
)

func TestWorkspaceInvalidation(t *testing.T) {
	e := &v1.WatchEvent{Path: "/workspace/new/child", OldPath: "/workspace/old/child", Operation: "rename", Type: v1.EntryType_ENTRY_TYPE_DIRECTORY, Sequence: 9007199254740993, PlacementEpoch: 7}
	out := workspaceInvalidation(e, 7, "/workspace")
	if out.Path != "new/child" || out.OldPath != "old/child" || out.EntryType != "directory" || out.Operation != "rename" || out.Reset {
		t.Fatalf("%+v", out)
	}
	e.Reset_ = true
	e.ResetReason = "initial"
	e.Sequence = 0
	out = workspaceInvalidation(e, 7, "/workspace")
	if !out.Reset || out.Reason != "initial" || out.Sequence != 0 {
		t.Fatalf("%+v", out)
	}
	e.Path = "/workspace/../../secret"
	out = workspaceInvalidation(e, 7, "/workspace")
	if !out.Reset || out.Reason != "invalid_path" || out.Path != "" || out.OldPath != "" {
		t.Fatalf("%+v", out)
	}
	e.PlacementEpoch = 8
	if out = workspaceInvalidation(e, 7, "/workspace"); out.Reason != "placement" {
		t.Fatalf("%+v", out)
	}
}
