package product

import (
	"harness-platform/orchestrator/internal/runtimerelease"
	"net/netip"
	"path/filepath"
	"testing"
)

func TestBootstrapFamiliesAndImmutableNetworks(t *testing.T) {
	seed := bootstrapSeedFixture(t)
	seed.PublicDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	work := seed.Sandbox
	work.Reference.ID = "sandbox-work-v1"
	work.TemplateID = "sandbox-work-v1"
	work.Manifest.Family = "work"
	work.Manifest.Name = runtimerelease.FamilyName("work")
	work.Manifest.Purpose = runtimerelease.FamilyPurpose("work")
	raw, err := runtimerelease.Bytes(work.Manifest)
	must(t, err)
	work.Reference.Digest, err = runtimerelease.Digest(raw)
	must(t, err)
	seed, err = seed.WithWorkRelease(work)
	must(t, err)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "bootstrap.db"))
	must(t, err)
	defer s.Close()
	admin, err := s.BootstrapAccount(ctx, "admin", "permanent-admin-password", seed)
	must(t, err)
	token := adminTestToken(t, s, admin)
	networks, err := s.ResourceTemplates(ctx, token, "network")
	must(t, err)
	if len(networks) != 2 {
		t.Fatal(networks)
	}
	for _, n := range networks {
		if !n.Builtin || !n.Enabled {
			t.Fatal(n)
		}
		if n.ID == "network-public" && (len(n.Network.Direct) != 2 || len(n.Network.Resolvers) != 1 || n.Network.Resolvers[0] != seed.PublicDNS[0]) {
			t.Fatal(n)
		}
		if n.ID == "network-restricted" && (len(n.Network.Direct) != 0 || len(n.Network.Resolvers) != 0) {
			t.Fatal(n)
		}
	}
	settings, err := s.ConfigurationSettings(ctx, token)
	must(t, err)
	if settings.SelectedNetwork.ID != "network-restricted" {
		t.Fatal(settings)
	}
	var publicGrants int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM policy_resource_bindings WHERE resource_kind='network_template' AND resource_id='network-public'`).Scan(&publicGrants))
	if publicGrants != 0 {
		t.Fatal("implicit Public grant")
	}
	environments, err := s.ResourceTemplates(ctx, token, "environment")
	must(t, err)
	if len(environments) != 2 {
		t.Fatal(environments)
	}
	for _, e := range environments {
		if e.ID == "work-environment" && e.Environment.SandboxRelease != work.Reference {
			t.Fatal("work reference not exact")
		}
	}
	_, err = s.DeleteResourceTemplate(ctx, token, "network", "network-restricted", AccountRevision{RequestID: "delete-built-in", Preview: true, ExpectedRevision: 1})
	wantError(t, err, ErrForbidden)
}
