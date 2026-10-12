package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func policyFixture(t *testing.T, cidr, resolver string) NetworkPolicy {
	t.Helper()
	r := Reachability{}
	if cidr != "" {
		r.Direct = []DirectRule{{CIDR: netip.MustParsePrefix(cidr), Port: 443, Protocol: "tcp"}}
	}
	if resolver != "" {
		r.Resolvers = []netip.Addr{netip.MustParseAddr(resolver)}
	}
	p, err := NewNetworkPolicy(r)
	must(t, err)
	return p
}
func pausePolicyProject(t *testing.T, l *Lifecycle, a Scope) {
	t.Helper()
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	must(t, l.Pause(ctx, a, p.Revision))
	drive(t, l, a.ProjectID)
}

func TestNetworkPolicyResumeSnapshotSurvivesLostReplyAndRestart(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	first := policyFixture(t, "192.0.2.0/24", "192.0.2.53")
	second := policyFixture(t, "198.51.100.0/24", "198.51.100.53")
	third := policyFixture(t, "", "")
	_, err := selectFixtureNetwork(t, l.Store, first)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, serviceProfile().Bindings()))
	drive(t, l, a.ProjectID)
	original, err := l.ActiveResources(ctx, a)
	must(t, err)
	if original.Profile.NetworkPolicyRevision != first.Revision {
		t.Fatal(original.Profile)
	}
	_, err = selectFixtureNetwork(t, l.Store, second)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	running, err := l.ActiveResources(ctx, a)
	must(t, err)
	if !reflect.DeepEqual(running, original) || f.calls["network"] != 0 || f.projections[original.Reference.SandboxID]["/etc/resolv.conf"] != "nameserver 192.0.2.53\n" {
		t.Fatal("reload touched running Project", running)
	}
	pausePolicyProject(t, l, a)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	_, err = selectFixtureNetwork(t, l.Store, third)
	must(t, err)
	f.fail["network"] = 1
	if err = l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("lost PATCH reply was hidden")
	}
	if f.calls["resume"] != 0 {
		t.Fatal("resumed before network commit")
	}
	var seq int
	var name, path string
	must(t, l.Store.reader.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&seq, &name, &path))
	must(t, l.Store.Close())
	reopened, err := openFixtureStore(ctx, path)
	must(t, err)
	defer reopened.Close()
	l.Store = reopened
	drive(t, l, a.ProjectID)
	resumed, err := l.ActiveResources(ctx, a)
	must(t, err)
	if resumed.Profile.NetworkPolicyRevision != second.Revision || resumed.VolumeForRole("workspace") != original.VolumeForRole("workspace") || resumed.VolumeForRole("runtime") != original.VolumeForRole("runtime") || resumed.Reference.SandboxID != original.Reference.SandboxID || f.calls["network"] != 1 {
		t.Fatal("resume lost original intent", resumed, f.calls)
	}
	if f.projections[resumed.Reference.SandboxID]["/etc/resolv.conf"] != "nameserver 198.51.100.53\n" || !strings.Contains(f.projections[resumed.Reference.SandboxID]["/etc/hosts"], "model.internal") {
		t.Fatal(f.projections)
	}
	pausePolicyProject(t, l, a)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	resumed, err = l.ActiveResources(ctx, a)
	must(t, err)
	if resumed.Profile.NetworkPolicyRevision != third.Revision || len(resumed.Profile.Reachability.Direct) != 0 || f.projections[resumed.Reference.SandboxID]["/etc/resolv.conf"] != "" {
		t.Fatal("permission removal did not apply", resumed)
	}
	calls := f.calls["network"]
	pausePolicyProject(t, l, a)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	if f.calls["network"] != calls {
		t.Fatal("unchanged policy mutated engine")
	}
}

func TestNetworkPolicyConflictingBindingsNeverResume(t *testing.T) {
	for _, mutation := range []string{"revision", "network", "capacity", "mount", "placement"} {
		t.Run(mutation, func(t *testing.T) {
			l, a, f := lifecycleFixture(t)
			first := policyFixture(t, "", "")
			second := policyFixture(t, "198.51.100.0/24", "")
			_, err := selectFixtureNetwork(t, l.Store, first)
			must(t, err)
			must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
			drive(t, l, a.ProjectID)
			pausePolicyProject(t, l, a)
			_, err = selectFixtureNetwork(t, l.Store, second)
			must(t, err)
			must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
			r, err := l.Store.Resources(ctx, a)
			must(t, err)
			id := r.Reference.SandboxID
			o := f.sandboxes[id]
			o.MutationRevision += "external"
			f.sandboxes[id] = o
			config := f.configurations[id]
			switch mutation {
			case "network":
				config.Network.Allow = []sandbox.Tuple{{CIDR: netip.MustParsePrefix("203.0.113.0/24"), Port: 80, Protocol: "tcp"}}
			case "capacity":
				config.MemoryBytes++
			case "mount":
				config.Mounts = append(config.Mounts, sandbox.Mount{VolumeID: "external", Destination: "/extra", Access: "shared-ro"})
			case "placement":
				o.Epoch++
				f.sandboxes[id] = o
			}
			f.configurations[id] = config
			wantError(t, l.Reconcile(ctx, a.ProjectID), ErrConflict)
			if f.calls["network"] != 0 || f.calls["resume"] != 0 {
				t.Fatal("conflict was overwritten", f.calls)
			}
			_, err = l.ActiveResources(ctx, a)
			wantError(t, err, ErrPending)
		})
	}
}

func TestNetworkPolicyConcurrentAdmissionAndProjectionFailure(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	policy := policyFixture(t, "192.0.2.0/24", "192.0.2.53")
	_, err := selectFixtureNetwork(t, l.Store, policy)
	must(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- l.EnsureActive(ctx, a, testProfile().Bindings()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	drive(t, l, a.ProjectID)
	pausePolicyProject(t, l, a)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	must(t, l.Reconcile(ctx, a.ProjectID))
	f.fail["projection"] = 1
	if err = l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("projection failure hidden")
	}
	_, err = l.ActiveResources(ctx, a)
	wantError(t, err, ErrPending)
	drive(t, l, a.ProjectID)
}

func TestNetworkPolicyFencedDuringHandoffAndCheckedBeforeWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	initial := policyFixture(t, "", "")
	accepted := policyFixture(t, "192.0.2.0/24", "")
	identity := ApplicationIdentity{ExecutableDigest: "sha256:" + strings.Repeat("a", 64), CompatibilityDigest: "sha256:" + strings.Repeat("c", 64)}
	s, err := OpenApplication(ctx, path, identity)
	must(t, err)
	_, err = selectFixtureNetwork(t, s, accepted)
	must(t, err)
	proof := func() GatewayObservation { return GatewayObservation{Started: time.Now()} }
	target := "sha256:" + strings.Repeat("b", 64)
	drain, err := s.BeginOwnerHandoff(ctx, "operator", time.Minute, target, identity.CompatibilityDigest, proof())
	must(t, err)
	if drain.RuntimeConfigurationDigest == "" {
		t.Fatal("configuration was not pinned")
	}
	_, err = selectFixtureNetwork(t, s, initial)
	wantError(t, err, ErrDraining)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, proof())
	must(t, err)
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, proof())
	must(t, err)
	must(t, s.Close())
	identity.ExecutableDigest = target
	s, err = OpenApplication(ctx, path, identity)
	must(t, err)
	defer s.Close()
	current, err := fixtureNetwork(ctx, s.reader)
	must(t, err)
	if current.Revision != accepted.Revision {
		t.Fatal("candidate replaced accepted database policy", current)
	}
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target, proof())
	must(t, err)
}

func TestNetworkPolicySnapshotIsValidatedAndDetached(t *testing.T) {
	p := policyFixture(t, "192.0.2.0/24", "192.0.2.53")
	s, _, _ := fixture(t)
	_, err := selectFixtureNetwork(t, s, p)
	must(t, err)
	p.Direct[0].Port = 80
	_, err = selectFixtureNetwork(t, s, p)
	wantError(t, err, ErrInvalid)
	got, err := fixtureNetwork(ctx, s.reader)
	must(t, err)
	if got.Direct[0].Port != 443 {
		t.Fatal("published policy mutated")
	}
	_, err = selectFixtureNetwork(t, s, NetworkPolicy{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestNetworkPolicyDeletionAfterLostPatchSettlesOriginalResources(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	first := policyFixture(t, "", "")
	second := policyFixture(t, "192.0.2.0/24", "")
	_, err := selectFixtureNetwork(t, l.Store, first)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	pausePolicyProject(t, l, a)
	_, err = selectFixtureNetwork(t, l.Store, second)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	f.fail["network"] = 1
	if l.Reconcile(ctx, a.ProjectID) == nil {
		t.Fatal("expected uncertain PATCH")
	}
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	must(t, l.Store.DeleteProject(ctx, a, p.Revision))
	drive(t, l, a.ProjectID)
	p, err = l.Store.Project(ctx, a)
	must(t, err)
	if p.State != "deleted" || len(f.sandboxes) != 0 || len(f.volumes) != 0 {
		t.Fatal("original resources leaked", p, f.sandboxes, f.volumes)
	}
}

func TestNetworkPolicyRecoveryAndRestartSelectLatestDefaults(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	first := policyFixture(t, "", "")
	second := policyFixture(t, "192.0.2.0/24", "")
	_, err := selectFixtureNetwork(t, l.Store, first)
	must(t, err)
	must(t, l.EnsureActive(ctx, a, testProfile().Bindings()))
	drive(t, l, a.ProjectID)
	original, err := l.ActiveResources(ctx, a)
	must(t, err)
	_, err = selectFixtureNetwork(t, l.Store, second)
	must(t, err)
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	must(t, l.Restart(ctx, a, p.Revision))
	drive(t, l, a.ProjectID)
	r, err := l.ActiveResources(ctx, a)
	must(t, err)
	if r.Profile.NetworkPolicyRevision != second.Revision || r.VolumeForRole("workspace") != original.VolumeForRole("workspace") {
		t.Fatal(r)
	}
	_, err = selectFixtureNetwork(t, l.Store, first)
	must(t, err)
	delete(f.sandboxes, r.Reference.SandboxID)
	must(t, l.Observe(ctx, a.ProjectID))
	drive(t, l, a.ProjectID)
	r, err = l.ActiveResources(ctx, a)
	must(t, err)
	if r.Profile.NetworkPolicyRevision != first.Revision || r.VolumeForRole("workspace") != original.VolumeForRole("workspace") || r.VolumeForRole("runtime") != original.VolumeForRole("runtime") {
		t.Fatal(r)
	}
}

func TestNetworkPolicyReloadAndActivationNeverMixRevisions(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	l.MaxProjects = 32
	policies := []NetworkPolicy{policyFixture(t, "192.0.2.0/24", "192.0.2.53"), policyFixture(t, "198.51.100.0/24", "198.51.100.53")}
	_, err := selectFixtureNetwork(t, l.Store, policies[0])
	must(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 33)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 32; i++ {
			if _, err := selectFixtureNetwork(t, l.Store, policies[i%2]); err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("policy-%d", i)
		_, err = l.Store.CreateProject(ctx, scope.Actor, id, "Concurrent policy")
		must(t, err)
		wg.Add(1)
		go func() { defer wg.Done(); errs <- l.EnsureActive(ctx, Scope{scope.Actor, id}, testProfile().Bindings()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	for i := 0; i < 16; i++ {
		r, err := l.Store.Resources(ctx, Scope{scope.Actor, fmt.Sprintf("policy-%d", i)})
		must(t, err)
		actual, err := NewNetworkPolicy(r.Profile.Reachability)
		must(t, err)
		if actual.Revision != r.Profile.NetworkPolicyRevision || (actual.Revision != policies[0].Revision && actual.Revision != policies[1].Revision) {
			t.Fatal("mixed policy", r.Profile)
		}
	}
}

// Lifecycle tests explicitly select a project network. Updating a catalog
// template alone no longer changes a retained project's network snapshot.
func selectFixtureNetwork(t *testing.T, s *Store, p NetworkPolicy) (NetworkPolicy, error) {
	t.Helper()
	checked, err := NewNetworkPolicy(Reachability{Direct: p.Direct, Resolvers: p.Resolvers})
	if err != nil || checked.Revision != p.Revision {
		return NetworkPolicy{}, ErrInvalid
	}
	defaults := checked
	raw, err := json.Marshal(p)
	if err != nil {
		return defaults, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		state, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if state.Phase != "open" {
			return ErrDraining
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM network_templates`).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO network_templates(id,name,policy,created_at,updated_at) VALUES('fixture-network','Fixture network',?,'','')`, string(raw)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE network_templates SET policy=?,revision=revision+1 WHERE id IN ('fixture-network','network-default')`, string(raw)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE project_configuration SET network=?,revision=revision+1`, string(raw))
		return err
	})
	return defaults, err
}

func fixtureNetwork(ctx context.Context, q queryer) (NetworkPolicy, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT policy FROM network_templates WHERE id IN ('fixture-network','network-default') ORDER BY id LIMIT 1`).Scan(&raw)
	if err != nil {
		return NetworkPolicy{}, err
	}
	var policy NetworkPolicy
	err = json.Unmarshal([]byte(raw), &policy)
	return policy, err
}
