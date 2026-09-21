package product

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

func serviceReachability() Reachability {
	return Reachability{
		Direct:    []DirectRule{{CIDR: netip.MustParsePrefix("203.0.113.0/24"), Port: 443, Protocol: "tcp"}},
		Services:  []ServiceRule{{Audience: "model", Destination: netip.MustParseAddr("10.80.0.1"), Port: 443, Gateway: netip.MustParseAddrPort("192.0.2.9:8443")}},
		Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53")},
		Hosts:     map[string]string{"model.internal": "model"},
	}
}
func serviceProfile() Profile {
	p := testProfile()
	p.Reachability = serviceReachability()
	return p
}

func TestReachabilityValidationRejectsUnsafeDeclarations(t *testing.T) {
	must(t, serviceReachability().validate())
	// A Project with no service at all is a supported zero-gateway deployment.
	must(t, Reachability{Direct: []DirectRule{{CIDR: netip.MustParsePrefix("203.0.113.0/24"), Port: 80, Protocol: "tcp"}}}.validate())
	must(t, Reachability{}.validate())
	for name, mutate := range map[string]func(*Reachability){
		"uncanonical direct prefix": func(r *Reachability) {
			r.Direct[0].CIDR = netip.MustParsePrefix("203.0.113.7/24")
		},
		"wildcard direct port":    func(r *Reachability) { r.Direct[0].Port = 0 },
		"unsupported protocol":    func(r *Reachability) { r.Direct[0].Protocol = "sctp" },
		"duplicate direct rule":   func(r *Reachability) { r.Direct = append(r.Direct, r.Direct[0]) },
		"duplicate audience":      func(r *Reachability) { r.Services = append(r.Services, r.Services[0]) },
		"public synthetic":        func(r *Reachability) { r.Services[0].Destination = netip.MustParseAddr("203.0.113.9") },
		"loopback synthetic":      func(r *Reachability) { r.Services[0].Destination = netip.MustParseAddr("127.0.0.1") },
		"loopback gateway":        func(r *Reachability) { r.Services[0].Gateway = netip.MustParseAddrPort("127.0.0.1:8443") },
		"gateway without port":    func(r *Reachability) { r.Services[0].Gateway = netip.MustParseAddrPort("192.0.2.9:0") },
		"service without port":    func(r *Reachability) { r.Services[0].Port = 0 },
		"undeclared host service": func(r *Reachability) { r.Hosts["model.internal"] = "other" },
		"invalid hostname":        func(r *Reachability) { r.Hosts["-bad-"] = "model" },
		"non IPv4 resolver":       func(r *Reachability) { r.Resolvers[0] = netip.MustParseAddr("2001:db8::53") },
		"service shadows direct": func(r *Reachability) {
			r.Direct = append(r.Direct, DirectRule{CIDR: netip.MustParsePrefix("10.80.0.1/32"), Port: 443, Protocol: "tcp"})
		},
	} {
		r := serviceReachability()
		mutate(&r)
		if err := r.validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		} else {
			wantError(t, err, ErrInvalid)
		}
	}
	// The profile rejects the same declarations, so no invalid reachability can be
	// reserved for a Project.
	bad := serviceProfile()
	bad.Reachability.Services[0].Gateway = netip.MustParseAddrPort("127.0.0.1:1")
	wantError(t, bad.validate(), ErrInvalid)
}

func TestReachabilityDeclarationAndProjections(t *testing.T) {
	r := serviceReachability()
	network := r.Declaration()
	if len(network.Allow) != 2 || len(network.Via) != 1 {
		t.Fatal(network)
	}
	if network.Allow[1] != (sandbox.Tuple{CIDR: netip.MustParsePrefix("192.0.2.53/32"), Port: 53, Protocol: "udp"}) {
		t.Fatal(network.Allow)
	}
	if network.Via[0].Endpoint != netip.MustParseAddrPort("192.0.2.9:8443") || network.Via[0].Match.CIDR != netip.MustParsePrefix("10.80.0.1/32") || network.Via[0].Match.Protocol != "tcp" {
		t.Fatal(network.Via)
	}
	hosts := r.HostsProjection()
	if !strings.Contains(hosts, "10.80.0.1\tmodel.internal\n") || !strings.Contains(hosts, "127.0.0.1\tlocalhost\n") {
		t.Fatal(hosts)
	}
	if got := r.ResolverProjection(); got != "nameserver 192.0.2.53\n" {
		t.Fatal(got)
	}
	// An empty declaration clears whatever the template shipped rather than
	// leaving a stale nameserver reachable through a default-deny network.
	if got := (Reachability{}).ResolverProjection(); got != "" {
		t.Fatal(got)
	}
}

func TestPlacementProjectionsWrittenOncePerEpochAndNotByChildren(t *testing.T) {
	l, scope, f := lifecycleFixture(t)
	f.fail["projection"] = 1
	must(t, l.EnsureActive(ctx, scope, serviceProfile()))
	// The lost projection reply retries against the original placement instead of
	// provisioning a second sandbox.
	settle(t, l, scope.ProjectID)
	r, err := l.ActiveResources(ctx, scope)
	must(t, err)
	if len(f.creates) != 1 {
		t.Fatal(f.creates)
	}
	if got := f.projections[r.Reference.SandboxID]["/etc/hosts"]; !strings.Contains(got, "model.internal") {
		t.Fatal(got)
	}
	if got := f.projections[r.Reference.SandboxID]["/etc/resolv.conf"]; got != "nameserver 192.0.2.53\n" {
		t.Fatal(got)
	}
	if f.epochs[r.Reference.SandboxID+"/etc/hosts"] != r.Reference.Epoch {
		t.Fatal(f.epochs)
	}
	writes := f.calls["projection"]
	// Creating children must not re-materialize reachability: a sibling Session
	// cannot observe its hostnames change underneath a running process.
	for _, id := range []string{"s1", "s2"} {
		_, err = l.Store.CreateSession(ctx, scope, id, "Agent")
		must(t, err)
	}
	_, err = l.Store.CreateTerminal(ctx, scope, "t1", "Terminal", 24, 80)
	must(t, err)
	must(t, l.EnsureActive(ctx, scope, serviceProfile()))
	if f.calls["projection"] != writes {
		t.Fatal("child creation rewrote Project reachability")
	}
	// A cold resume is a new placement with a fresh guest filesystem, so the same
	// immutable declaration is re-materialized under the successor epoch.
	p, err := l.Store.Project(ctx, scope)
	must(t, err)
	must(t, l.Restart(ctx, scope, p.Revision))
	drive(t, l, scope.ProjectID)
	resumed, err := l.ActiveResources(ctx, scope)
	must(t, err)
	if resumed.Reference.Epoch <= r.Reference.Epoch {
		t.Fatal(r.Reference, resumed.Reference)
	}
	if f.epochs[resumed.Reference.SandboxID+"/etc/hosts"] != resumed.Reference.Epoch {
		t.Fatal("resumed placement kept a stale projection", f.epochs)
	}
	if got := f.projections[resumed.Reference.SandboxID]["/etc/hosts"]; !strings.Contains(got, "10.80.0.1\tmodel.internal") {
		t.Fatal(got)
	}
}

func TestServiceGrantAttributionRevocationAndTurnDrain(t *testing.T) {
	s, scope, i, c := agentServiceFixture(t)
	grant, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	if grant.SessionID != i.SessionID || grant.IncarnationID != i.ID {
		t.Fatal(grant)
	}
	// Attribution is host-derived: the guest's credential names no turn, and the
	// store resolves the incarnation's currently admitted turn instead.
	a, err := s.AuthorizeService(ctx, "model", token)
	must(t, err)
	if a.TurnID != c.TurnID || a.Grant.ProjectID != scope.ProjectID || a.TurnState != "starting" {
		t.Fatal(a)
	}
	must(t, s.HoldsAttribution(ctx, a))
	_, err = s.AuthorizeService(ctx, "wrong", token)
	wantError(t, err, ErrNotFound)
	_, err = s.AuthorizeService(ctx, "model", token+"a")
	wantError(t, err, ErrNotFound)
	_, err = s.AuthorizeService(ctx, "model", grant.ID+".0000")
	wantError(t, err, ErrNotFound)
	// An undeclared audience cannot be granted even for a live incarnation.
	_, _, err = s.GrantService(ctx, scope, i.SessionID, i.ID, "undeclared", time.Hour)
	wantError(t, err, ErrInvalid)
	// One live grant per incarnation and audience; a replayed start reuses it.
	_, _, err = s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	wantError(t, err, ErrConflict)
	_, _, err = s.GrantService(ctx, Scope{"bob", scope.ProjectID}, i.SessionID, i.ID, "model", time.Hour)
	wantError(t, err, ErrNotFound)

	// Completing the turn drains that request without revoking the Session's
	// grant: the credential belongs to the incarnation, not to one turn.
	_, err = s.db.Exec(`UPDATE turns SET state='completed' WHERE id=?`, c.TurnID)
	must(t, err)
	wantError(t, s.HoldsAttribution(ctx, a), ErrNotFound)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	live, err := s.ServiceGrants(ctx, scope)
	must(t, err)
	if len(live) != 1 || live[0].State != "active" {
		t.Fatal(live)
	}
	// A later turn is a fresh attribution under the same grant, never an
	// inheritance of the completed one.
	next, err := s.AdmitTurn(ctx, scope, i.SessionID, "second", "again", false)
	must(t, err)
	_, err = s.db.Exec(`UPDATE turns SET state='running' WHERE id=?`, next.ID)
	must(t, err)
	successor, err := s.AuthorizeService(ctx, "model", token)
	must(t, err)
	if successor.TurnID != next.ID {
		t.Fatal(successor)
	}
	// The old request still drains: it is pinned to its own turn.
	wantError(t, s.HoldsAttribution(ctx, a), ErrNotFound)
	must(t, s.HoldsAttribution(ctx, successor))
}

func TestServiceAuthorityFollowsProjectIncarnationAndExpiry(t *testing.T) {
	s, scope, i, c := agentServiceFixture(t)
	live := func(t *testing.T) (Attribution, string) {
		t.Helper()
		_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
		must(t, err)
		a, err := s.AuthorizeService(ctx, "model", token)
		must(t, err)
		return a, token
	}
	revive := func(t *testing.T) {
		t.Helper()
		_, err := s.db.Exec(`UPDATE service_grants SET state='revoked' WHERE state='active'`)
		must(t, err)
		_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, i.ID)
		must(t, err)
		_, err = s.db.Exec(`UPDATE projects SET state='active' WHERE id=?`, scope.ProjectID)
		must(t, err)
		_, err = s.db.Exec(`UPDATE turns SET state='running' WHERE id=?`, c.TurnID)
		must(t, err)
	}
	// An exited or replaced incarnation revokes its grant by trigger.
	_, token := live(t)
	_, err := s.db.Exec(`UPDATE agent_incarnations SET state='exited' WHERE id=?`, i.ID)
	must(t, err)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	revive(t)

	// A Project that leaves active revokes every grant, so a paused Project's
	// leftover credential reaches nothing when it resumes.
	_, token = live(t)
	_, err = s.db.Exec(`UPDATE projects SET state='pausing' WHERE id=?`, scope.ProjectID)
	must(t, err)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	revive(t)

	// Project deletion closes service authority with admission.
	_, token = live(t)
	p, err := s.Project(ctx, scope)
	must(t, err)
	must(t, s.DeleteProject(ctx, scope, p.Revision))
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	_, _, err = s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	wantError(t, err, ErrDeleting)
	revive(t)

	// Expiry bounds authority even while everything else stays live.
	now := time.Now()
	s.now = func() time.Time { return now }
	_, token = live(t)
	now = now.Add(2 * time.Hour)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	_, _, err = s.GrantService(ctx, scope, i.SessionID, i.ID, "model", 48*time.Hour)
	wantError(t, err, ErrInvalid)
}

func TestIdleAgentHasNoServiceAuthorityWithoutAdmittedTurn(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, scope, serviceProfile()))
	drive(t, l, scope.ProjectID)
	s := l.Store
	_, err := s.CreateSession(ctx, scope, "idle", "Agent")
	must(t, err)
	i, err := s.reserveAgent(ctx, scope, "idle")
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, i.ID)
	must(t, err)
	_, token, err := s.GrantService(ctx, scope, "idle", i.ID, "model", time.Hour)
	must(t, err)
	// The runner holds a valid credential, but with no admitted turn there is no
	// service authority at all: an idle agent cannot reach an upstream alone.
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	turn, err := s.AdmitTurn(ctx, scope, "idle", "first", "hello", false)
	must(t, err)
	// A queued turn is admitted for Project activity but is not yet dispatched, so
	// it grants no request authority either.
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
	_, err = s.db.Exec(`UPDATE turns SET state='starting' WHERE id=?`, turn.ID)
	must(t, err)
	a, err := s.AuthorizeService(ctx, "model", token)
	must(t, err)
	if a.TurnID != turn.ID {
		t.Fatal(a)
	}
	// A Terminal never mints a turn and receives no credential, so generic
	// reachability stays Project-scoped.
	terminal, err := s.CreateTerminal(ctx, scope, "term", "Terminal", 24, 80)
	must(t, err)
	grants, err := s.ServiceGrants(ctx, scope)
	must(t, err)
	if len(grants) != 1 || grants[0].SessionID != "idle" {
		t.Fatal(terminal, grants)
	}
}

func TestServiceRenewalFencesOwnerAndNeverRevivesExpiredGrant(t *testing.T) {
	s, scope, i, _ := agentServiceFixture(t)
	clock := s.now()
	s.now = func() time.Time { return clock }
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	clock = clock.Add(40 * time.Minute)
	must(t, s.RenewService(ctx, i, "model", time.Hour))
	clock = clock.Add(30 * time.Minute)
	_, err = s.AuthorizeService(ctx, "model", token)
	must(t, err)
	stale := i
	stale.Owner++
	wantError(t, s.RenewService(ctx, stale, "model", time.Hour), ErrConflict)
	clock = clock.Add(time.Hour)
	wantError(t, s.RenewService(ctx, i, "model", time.Hour), ErrNotFound)
	_, err = s.AuthorizeService(ctx, "model", token)
	wantError(t, err, ErrNotFound)
}
