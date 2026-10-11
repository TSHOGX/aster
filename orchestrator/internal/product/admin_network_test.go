package product

import (
	"net/netip"
	"testing"
)

func TestAdminNetworkDefaultsUseRevisionsReceiptsAndAudit(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	before, err := s.AdminNetworkDefaults(ctx, token)
	must(t, err)
	input := NetworkDefaultEdit{RequestID: "network-edit", ExpectedRevision: before.ConfigurationRevision, Direct: []DirectRule{{CIDR: netip.MustParsePrefix("192.0.2.0/24"), Port: 443, Protocol: "tcp"}}, Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53")}}
	edited, err := s.EditNetworkDefaults(ctx, token, input)
	must(t, err)
	if edited.ConfigurationRevision != before.ConfigurationRevision+1 || edited.Revision == before.Revision {
		t.Fatal(edited)
	}
	replay, err := s.EditNetworkDefaults(ctx, token, input)
	must(t, err)
	if !replay.Replayed || replay.ConfigurationRevision != edited.ConfigurationRevision {
		t.Fatal(replay)
	}
	input.RequestID = "stale"
	_, err = s.EditNetworkDefaults(ctx, token, input)
	wantError(t, err, ErrConflict)
	input.ExpectedRevision = edited.ConfigurationRevision
	input.RequestID = "invalid"
	input.Direct[0].Protocol = "all"
	_, err = s.EditNetworkDefaults(ctx, token, input)
	wantError(t, err, ErrInvalid)
	input.Direct = []DirectRule{}
	input.Resolvers = []netip.Addr{}
	input.RequestID = "rollback"
	_, err = s.db.Exec(`CREATE TRIGGER network_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='network.default.update' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	must(t, err)
	if _, err = s.EditNetworkDefaults(ctx, token, input); err == nil {
		t.Fatal("audit failure ignored")
	}
	retained, err := s.NetworkDefaults(ctx)
	must(t, err)
	if retained.ConfigurationRevision != edited.ConfigurationRevision || retained.Revision != edited.Revision {
		t.Fatal("partial network update committed", retained)
	}
	_, err = s.db.Exec(`DROP TRIGGER network_audit_fault`)
	must(t, err)
	created, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "ordinary-network", Username: "ordinary", DisplayName: "Ordinary", Role: "user"})
	must(t, err)
	_, temporary, err := s.Authenticate(ctx, created.User.Username, created.TemporaryPassword)
	must(t, err)
	_, ordinary, err := s.ChangePassword(ctx, temporary, "", "ordinary-permanent-password", true)
	must(t, err)
	_, err = s.AdminNetworkDefaults(ctx, ordinary)
	wantError(t, err, ErrForbidden)
	_, err = s.EditNetworkDefaults(ctx, ordinary, input)
	wantError(t, err, ErrForbidden)
	var audit int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action='network.default.update'`).Scan(&audit))
	if audit != 1 {
		t.Fatal(audit)
	}
}
