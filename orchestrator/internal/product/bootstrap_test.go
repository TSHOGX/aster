package product

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestBootstrapSeedsRollbackAndSingleInitialization(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seed := bootstrapSeedFixture(t)
	// A no-account owner can already have its explicitly declared network state.
	if _, err = s.InitializeNetworkPolicy(ctx, seed.Network); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TEMP TRIGGER fail_bootstrap_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BootstrapAccount(ctx, "admin", "administrator-password", seed); err == nil {
		t.Fatal("injected audit failure ignored")
	}
	for _, table := range []string{"accounts", "account_groups", "permission_policies", "account_memberships", "group_resource_policy", "platform_resource_limits", "default_environment", "registered_sandboxes"} {
		var count int
		if err = s.reader.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial bootstrap", table, count, err)
		}
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_bootstrap_audit`); err != nil {
		t.Fatal(err)
	}
	account, err := s.BootstrapAccount(ctx, "admin", "administrator-password", seed)
	if err != nil {
		t.Fatal(err)
	}
	var group string
	if err = s.reader.QueryRowContext(ctx, `SELECT group_id FROM account_memberships WHERE account_id=?`, account.ID).Scan(&group); err != nil || group != StandardUsersGroup {
		t.Fatal(group, err)
	}
	var statements string
	if err = s.reader.QueryRowContext(ctx, `SELECT statements FROM permission_policies WHERE id=?`, standardUsersPolicy).Scan(&statements); err != nil {
		t.Fatal(err)
	}
	var policy map[ProductAction]PolicyEffect
	if err = json.Unmarshal([]byte(statements), &policy); err != nil {
		t.Fatal(err)
	}
	if policy[ActionFilesRead] != PolicyAllow || policy[ActionAdminContentRead] == PolicyAllow || policy[ActionPublicSharesCreate] == PolicyAllow {
		t.Fatal(policy)
	}
	var initial, ceiling int64
	if err = s.reader.QueryRowContext(ctx, `SELECT default_value,ceiling FROM group_resource_policy WHERE group_id=? AND dimension=?`, StandardUsersGroup, ResourcePersistent).Scan(&initial, &ceiling); err != nil {
		t.Fatal(err)
	}
	if initial != 10*(seed.Resources.Workspace+seed.Resources.Runtime) || ceiling != 100*(seed.Resources.Workspace+seed.Resources.Runtime) {
		t.Fatal(initial, ceiling)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE group_resource_policy SET default_value=1 WHERE dimension='projects'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BootstrapAccount(ctx, "replacement", "different-admin-password", seed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.reader.QueryRowContext(ctx, `SELECT default_value FROM group_resource_policy WHERE dimension='projects'`).Scan(&initial); err != nil || initial != 1 {
		t.Fatal("bootstrap overwrote edited settings", initial, err)
	}
}
func TestBootstrapRejectsUnverifiedSeedBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seed := bootstrapSeedFixture(t)
	seed.Sandbox.Manifest.Workspace = "/other"
	if _, err = s.BootstrapAccount(ctx, "admin", "administrator-password", seed); err == nil {
		t.Fatal("unverified release metadata accepted")
	}
	var count int
	if err = s.reader.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
