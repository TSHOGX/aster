package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPlatformFoundationDefaultsAndAudit(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	flags, err := st.ListFeatureFlags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 2 || flags[0].Enabled {
		t.Fatalf("unexpected defaults: %+v", flags)
	}
	if err := st.EnsureUserWithRole(ctx, "alice", "Alice", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureUserWithRole(ctx, "alice", "Alice", RoleUser); err != nil {
		t.Fatal(err)
	}
	role, err := st.UserRole(ctx, "alice")
	if err != nil || role != RoleAdmin {
		t.Fatalf("role=%q err=%v", role, err)
	}
	updated, err := st.SetFeatureFlag(ctx, "cron_agent", true, "test", "alice", time.Now())
	if err != nil || !updated.Enabled {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if err := st.AppendAudit(ctx, AuditEntry{ActorUserID: "alice", Action: "test"}); err != nil {
		t.Fatal(err)
	}
	entries, err := st.ListAudit(ctx, 10, 0)
	if err != nil || len(entries) != 1 || entries[0].ActorUserID != "alice" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}
