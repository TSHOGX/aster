package testfixture

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/sqlitex"
)

var identityHash = sync.OnceValues(func() (string, error) { return auth.HashPasswordWithIterations("fixture-password", 1000) })

// SeedIdentities authors explicit current-model test data in an already-owned
// transaction. IDs are immutable fixture labels, distinct from login usernames.
// The closed policy grants the operations these fixtures exercise, including
// explicit public sharing. It is not a production bootstrap or an auth bypass.
func SeedIdentities(ctx context.Context, tx *sql.Tx) error {
	hash, err := identityHash()
	if err != nil {
		return err
	}
	now := sqlitex.FormatTime(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_groups(id,name,description,created_at,updated_at) VALUES('fixture-users','Fixture users','Isolated service test identities',?,?)`, now, now); err != nil {
		return err
	}
	actions := []string{"projects.read", "projects.create", "projects.manage", "files.list", "files.read", "files.export", "files.upload", "files.edit", "files.manage", "conversations.share", "shares.public.create", "terminals.use", "agents.run", "schedules.manage"}
	statements := map[string]string{}
	for _, action := range actions {
		statements[action] = "allow"
	}
	raw, _ := json.Marshal(statements)
	if _, err = tx.ExecContext(ctx, `INSERT INTO permission_policies(id,name,statements,created_at,updated_at) VALUES('fixture-policy','Fixture operations',?,?,?)`, string(raw), now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO group_policy_bindings(group_id,policy_id) VALUES('fixture-users','fixture-policy')`); err != nil {
		return err
	}
	// Explicit finite fixture entitlements. Real bootstrap remains separate;
	// production never substitutes these when numeric authority is missing.
	limits := map[string]int64{"projects": 10000, "running_projects": 128, "cpu_millis": 64000, "memory_bytes": 64 << 30, "overlay_bytes": 64 << 30, "workspace_bytes": 64 << 30, "runtime_bytes": 64 << 30, "aggregate_cpu_millis": 8192000, "aggregate_memory_bytes": 8 << 40, "owned_volume_bytes": 64 << 30, "project_owned_volume_bytes": 128 << 30, "mount_count": 128, "persistent_bytes": 1 << 50, "agents": 256, "terminals": 256, "schedules": 10000, "transfer_bytes": 256 << 20}
	for dimension, value := range limits {
		if _, err = tx.ExecContext(ctx, `INSERT INTO group_resource_policy(group_id,dimension,default_value,ceiling) VALUES('fixture-users',?,?,?)`, dimension, value, value); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO platform_resource_limits(dimension,maximum) VALUES(?,?)`, dimension, value); err != nil {
			return err
		}
	}
	for _, id := range []string{"alice", "bob", "carol", "owner", "reader", "user", "researcher", "other"} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO accounts(id,username,display_name,role,password_hash,created_at,updated_at) VALUES(?,?,?,'user',?,?,?)`, id, "fixture-"+id, id, hash, now, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_memberships(account_id,group_id) VALUES(?,'fixture-users')`, id); err != nil {
			return err
		}
	}
	return seedDefaultRuntime(ctx, tx)
}
