package store

import (
	"context"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/workspace"
)

func TestPreparedContentReferencesFollowMutationAndMaintenanceSettlement(t *testing.T) {
	ctx := context.Background()
	st, _, b, _ := grantFixture(t)
	content := workspace.PreparedContent{Digest: strings.Repeat("a", 64), Size: 12}
	mutation, err := st.BeginGuestWorkspaceMutation(ctx, workspace.Mutation{ID: "upload", SessionID: b.SessionID, Owner: b.Owner, Path: "file.txt"}, content, 2)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		used, err := st.PreparedContentReferenced(ctx, b.Owner, content.Digest)
		if err != nil || used != want {
			t.Fatalf("prepared reference = %t want %t: %v", used, want, err)
		}
	}
	check(true)
	if _, err := st.PreparedContentReferenced(ctx, "foreign-owner", content.Digest); err == nil {
		t.Fatal("foreign owner acquired collection authority")
	}
	if used, err := st.PreparedContentReferenced(ctx, b.Owner, strings.Repeat("b", 64)); err != nil || used {
		t.Fatalf("unrelated content incorrectly referenced: %t %v", used, err)
	}
	if err := st.MarkGuestWorkspaceMutationIssued(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.State, mutation.Error = "uncertain", "lost helper reply"
	if err := st.FinishWorkspaceMutation(ctx, mutation.Mutation); err != nil {
		t.Fatal(err)
	}
	check(true)
	mutation.State = "failed"
	if err := st.FinishWorkspaceMutation(ctx, mutation.Mutation); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := st.db.Exec(`INSERT INTO workspace_write_maintenance(mutation_id,environment_id,stage) VALUES(?,?,'pausing')`, mutation.ID, b.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := st.db.Exec(`UPDATE workspace_write_maintenance SET stage='complete' WHERE mutation_id=?`, mutation.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
	var digest string
	if err := st.db.QueryRow(`SELECT content_digest FROM workspace_guest_mutations WHERE operation_id=?`, mutation.ID).Scan(&digest); err != nil || digest != content.Digest {
		t.Fatalf("collection lookup mutated historical receipt: %s %v", digest, err)
	}
}
