package product

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestUserProjectConfigurationsRetainsSnapshotsAndFiltersOwnership(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	// A single reader also guards against issuing nested queries with rows open.
	s.reader.SetMaxOpenConns(1)
	items, err := s.UserProjectConfigurations(ctx, token)
	must(t, err)
	if items == nil || len(items) != 0 {
		t.Fatalf("empty list: %+v", items)
	}
	other, _, err := createTestAccount(t, s, admin.ID, "other", "Other", "user")
	must(t, err)
	for _, id := range []string{"older", "newer", "deleted", "other-owner"} {
		_, err = s.CreateProject(ctx, admin.ID, id, id)
		must(t, err)
	}
	retained, err := projectConfiguration(ctx, s.reader, "newer")
	must(t, err)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`UPDATE projects SET state='deleted' WHERE id='deleted'`,
			`UPDATE projects SET updated_at='2026-01-01T00:00:00Z' WHERE id='older'`,
			`UPDATE projects SET state='paused',execution_suspended=1 WHERE id='newer'`,
			`UPDATE network_templates SET name='Renamed network',policy='{}' WHERE id='network-default'`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE projects SET owner_id=? WHERE id='other-owner'`, other.ID)
		return err
	}))
	items, err = s.UserProjectConfigurations(ctx, token)
	must(t, err)
	if len(items) != 2 || items[0].ProjectID != "newer" || items[1].ProjectID != "older" {
		t.Fatalf("ownership, deletion or ordering: %+v", items)
	}
	got := items[0]
	if got.Title != "newer" || got.State != "paused" || !got.Suspended || got.NetworkName != "Renamed network" || got.EnvironmentName == "" {
		t.Fatalf("current metadata: %+v", got)
	}
	if !reflect.DeepEqual(got.Network, retained.Network) || !reflect.DeepEqual(got.Environment, retained.Environment) || got.Revision != retained.Revision {
		t.Fatalf("catalog edit changed retained configuration: %+v", got)
	}
	must(t, s.RevokeLogin(ctx, token))
	_, err = s.UserProjectConfigurations(ctx, token)
	wantError(t, err, ErrUnauthorized)
}
