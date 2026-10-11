package product

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestUserNetworkRevisionInheritanceAndAtomicAudit(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	before, err := s.AdminUserNetwork(ctx, token, admin.ID)
	must(t, err)
	if before.Mode != "inherit" || before.ConfigurationRevision != 0 || before.Override != nil {
		t.Fatal(before)
	}
	input := UserNetworkEdit{RequestID: "override", ExpectedRevision: 0, ExpectedDefaultRevision: before.DefaultConfigurationRevision, Mode: "override", Direct: []DirectRule{}, Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53")}}
	edited, err := s.EditUserNetwork(ctx, token, admin.ID, input)
	must(t, err)
	if edited.Mode != "override" || edited.ConfigurationRevision != 1 || edited.Override == nil {
		t.Fatal(edited)
	}
	defaults, err := s.EditNetworkDefaults(ctx, token, NetworkDefaultEdit{RequestID: "new-default", ExpectedRevision: before.DefaultConfigurationRevision, Direct: []DirectRule{}, Resolvers: []netip.Addr{}})
	must(t, err)
	retained, err := s.AdminUserNetwork(ctx, token, admin.ID)
	must(t, err)
	if retained.Desired.Revision != edited.Desired.Revision || retained.DefaultConfigurationRevision != defaults.ConfigurationRevision {
		t.Fatal(retained)
	}
	replay, err := s.EditUserNetwork(ctx, token, admin.ID, input)
	must(t, err)
	if !replay.Replayed || replay.ConfigurationRevision != 1 {
		t.Fatal(replay)
	}
	input.Mode = "inherit"
	input.Direct = nil
	input.Resolvers = nil
	_, err = s.EditUserNetwork(ctx, token, admin.ID, input)
	wantError(t, err, ErrConflict)
	input.RequestID = "stale"
	_, err = s.EditUserNetwork(ctx, token, admin.ID, input)
	wantError(t, err, ErrConflict)
	input.ExpectedRevision = 1
	_, err = s.EditUserNetwork(ctx, token, admin.ID, input)
	wantError(t, err, ErrConflict)
	input.ExpectedDefaultRevision = defaults.ConfigurationRevision
	_, err = s.db.Exec(`CREATE TRIGGER network_user_fault BEFORE INSERT ON audit_events WHEN NEW.action='network.user.update' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	must(t, err)
	if _, err = s.EditUserNetwork(ctx, token, admin.ID, input); err == nil {
		t.Fatal("audit failure ignored")
	}
	retained, err = s.AdminUserNetwork(ctx, token, admin.ID)
	must(t, err)
	if retained.ConfigurationRevision != 1 || retained.Mode != "override" {
		t.Fatal("partial commit", retained)
	}
	_, err = s.db.Exec(`DROP TRIGGER network_user_fault`)
	must(t, err)
	inherited, err := s.EditUserNetwork(ctx, token, admin.ID, input)
	must(t, err)
	if inherited.Mode != "inherit" || inherited.Override != nil || inherited.Desired.Revision != defaults.Revision || inherited.ConfigurationRevision != 2 {
		t.Fatal(inherited)
	}
	_, err = s.AdminUserNetwork(ctx, token, "missing")
	wantError(t, err, ErrNotFound)
	input.RequestID = "missing-account"
	_, err = s.EditUserNetwork(ctx, token, "missing", input)
	wantError(t, err, ErrNotFound)
	_, err = s.db.Exec(`UPDATE application_release SET phase='draining'`)
	must(t, err)
	input.RequestID = "fenced"
	input.ExpectedRevision = 2
	_, err = s.EditUserNetwork(ctx, token, admin.ID, input)
	wantError(t, err, ErrDraining)
}

func TestUserNetworkCreationResumeRetainsEnvironmentAndRunningPolicy(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	_, err := l.Store.db.Exec(`UPDATE accounts SET role='admin' WHERE id=?`, a.Actor)
	must(t, err)
	account, err := l.Store.Account(ctx, a.Actor)
	must(t, err)
	_, token, err := l.Store.Authenticate(ctx, account.Username, "fixture-password")
	must(t, err)
	defaults, err := l.Store.NetworkDefaults(ctx)
	must(t, err)
	input := UserNetworkEdit{RequestID: "user-first", ExpectedDefaultRevision: defaults.ConfigurationRevision, Mode: "override", Direct: []DirectRule{}, Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53")}}
	first, err := l.Store.EditUserNetwork(ctx, token, a.Actor, input)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, serviceProfile().Bindings()))
	drive(t, l, a.ProjectID)
	original, err := l.ActiveResources(ctx, a)
	must(t, err)
	if original.Profile.NetworkPolicyRevision != first.Desired.Revision {
		t.Fatal(original.Profile)
	}
	input.ExpectedRevision = first.ConfigurationRevision
	input.RequestID = "user-second"
	input.Resolvers = []netip.Addr{}
	second, err := l.Store.EditUserNetwork(ctx, token, a.Actor, input)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, serviceProfile().Bindings()))
	unchanged, err := l.ActiveResources(ctx, a)
	must(t, err)
	if !reflect.DeepEqual(original, unchanged) || f.calls["network"] != 0 {
		t.Fatal("edit changed running placement")
	}
	pausePolicyProject(t, l, a)
	must(t, l.EnsureActive(ctx, a, serviceProfile().Bindings()))
	drive(t, l, a.ProjectID)
	resumed, err := l.ActiveResources(ctx, a)
	must(t, err)
	if resumed.Profile.NetworkPolicyRevision != second.Desired.Revision || !reflect.DeepEqual(resumed.Profile.Volumes, original.Profile.Volumes) || resumed.Profile.SandboxRelease != original.Profile.SandboxRelease {
		t.Fatal(resumed)
	}
	// A global policy conflicting with this Project's service route is allowed
	// while its owner overrides; switching back to inheritance must reject it.
	service := original.Profile.Reachability.Services[0]
	conflicting, err := l.Store.EditNetworkDefaults(ctx, token, NetworkDefaultEdit{RequestID: "default-conflict", ExpectedRevision: defaults.ConfigurationRevision, Direct: []DirectRule{{CIDR: netip.PrefixFrom(service.Destination, 32), Port: service.Port, Protocol: "tcp"}}, Resolvers: []netip.Addr{}})
	must(t, err)
	input = UserNetworkEdit{RequestID: "inherit-conflict", ExpectedRevision: second.ConfigurationRevision, ExpectedDefaultRevision: conflicting.ConfigurationRevision, Mode: "inherit"}
	_, err = l.Store.EditUserNetwork(ctx, token, a.Actor, input)
	wantError(t, err, ErrInvalid)
}
