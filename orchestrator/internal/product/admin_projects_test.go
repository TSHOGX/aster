package product

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/sandbox"
	"net/netip"
	"strings"
	"testing"
)

type projectMetadataInspector struct {
	backend     *fakeLifecycle
	unavailable bool
}

func (o projectMetadataInspector) InspectConfiguration(ctx context.Context, ref sandbox.Reference) (sandbox.Configuration, error) {
	if o.unavailable {
		return sandbox.Configuration{}, ErrPending
	}
	return o.backend.InspectConfiguration(ctx, ref)
}
func (o projectMetadataInspector) InspectVolume(_ context.Context, ref sandbox.VolumeReference) (sandbox.Volume, error) {
	if o.unavailable {
		return sandbox.Volume{}, ErrPending
	}
	volume, ok := o.backend.volumes[ref.VolumeID]
	if !ok {
		return sandbox.Volume{}, &sandbox.Error{Class: sandbox.NotFound}
	}
	return volume, nil
}
func TestAdminProjectMetadataDoesNotProvisionOrDisclosePrivateContent(t *testing.T) {
	l, scope, backend := lifecycleFixture(t)
	s := l.Store
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id=?`, scope.Actor)
	must(t, err)
	admin, err := s.Account(ctx, scope.Actor)
	must(t, err)
	_, token, err := s.Authenticate(ctx, admin.Username, "fixture-password")
	must(t, err)
	inspector := projectMetadataInspector{backend: backend}
	cold, err := s.AdminProject(ctx, token, scope.ProjectID, inspector)
	must(t, err)
	if cold.Bound || cold.Binding != nil || cold.Observation.Status != "unbound" || len(backend.volumes) != 0 || len(backend.creates) != 0 {
		t.Fatal(cold)
	}
	local := &localFileBackend{root: t.TempDir(), processes: map[string]*localFileProcess{}}
	files := &Files{Store: s, Lifecycle: l, Backend: local, Bindings: serviceProfile().Bindings()}
	defer files.Close()
	_, err = files.Upload(ctx, scope, "private-upload", writeRequest("private-name", "private-content-sentinel"), strings.NewReader("private-content-sentinel"))
	must(t, err)
	var before, after int64
	must(t, s.db.QueryRow(`SELECT total_changes()`).Scan(&before))
	view, err := s.AdminProject(ctx, token, scope.ProjectID, inspector)
	must(t, err)
	must(t, s.db.QueryRow(`SELECT total_changes()`).Scan(&after))
	if before != after || len(backend.creates) != 0 || view.AllocatedBytes != 64<<20 || view.ReservedBytes != 32<<20 || view.Binding.Occupied || view.Observation.Status != "not_materialized" {
		t.Fatal(before, after, view)
	}
	for _, slot := range view.Binding.Slots {
		if slot.Role == "workspace" && slot.State != "ready" || slot.Role == "runtime" && slot.State != "reserved" {
			t.Fatal(view.Binding.Slots)
		}
	}

	raw, err := json.Marshal(view)
	must(t, err)
	for _, private := range []string{"private-name", "private-content-sentinel", "model.internal", "gateway", "services", "hosts", "intent_key", "password_hash"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private data leaked", private, string(raw))
		}
	}
	must(t, l.EnsureActive(ctx, scope, serviceProfile().Bindings()))
	pending, err := s.AdminProject(ctx, token, scope.ProjectID, inspector)
	must(t, err)
	if pending.Operation == nil || pending.Operation.State == "succeeded" || !pending.NetworkPending || pending.Observation.NetworkRevision != "" {
		t.Fatal(pending)
	}
	drive(t, l, scope.ProjectID)
	active, err := s.AdminProject(ctx, token, scope.ProjectID, inspector)
	must(t, err)
	if active.Observation.NetworkRevision != active.SavedNetworkRevision || active.Observation.MatchesSavedBinding == nil || !*active.Observation.MatchesSavedBinding || active.ReservedBytes != 0 {
		t.Fatal(active)
	}
	settings, err := s.AdminUserNetwork(ctx, token, scope.Actor)
	must(t, err)
	_, err = s.EditUserNetwork(ctx, token, scope.Actor, UserNetworkEdit{RequestID: "new-network", ExpectedRevision: settings.ConfigurationRevision, ExpectedDefaultRevision: settings.DefaultConfigurationRevision, Mode: "override", Direct: []DirectRule{}, Resolvers: []netip.Addr{netip.MustParseAddr("192.0.2.53")}})
	must(t, err)
	changed, err := s.AdminProject(ctx, token, scope.ProjectID, inspector)
	must(t, err)
	if !changed.NetworkPending || changed.Observation.NetworkRevision != active.SavedNetworkRevision || changed.DesiredNetwork.Revision == changed.SavedNetworkRevision {
		t.Fatal(changed)
	}
	unavailable, err := s.AdminProject(ctx, token, scope.ProjectID, projectMetadataInspector{backend: backend, unavailable: true})
	must(t, err)
	if unavailable.Observation.Status != "unavailable" || unavailable.Observation.NetworkRevision != "" || unavailable.AllocatedBytes != 96<<20 {
		t.Fatal(unavailable)
	}
	first, err := s.AdminProjects(ctx, token, AdminProjectQuery{Limit: 1})
	must(t, err)
	if len(first.Projects) != 1 || first.NextCursor == "" {
		t.Fatal(first)
	}
	second, err := s.AdminProjects(ctx, token, AdminProjectQuery{Limit: 1, Cursor: first.NextCursor})
	must(t, err)
	if len(second.Projects) != 1 || second.Projects[0].ID == first.Projects[0].ID {
		t.Fatal(second)
	}
	_, err = s.AdminProjects(ctx, token, AdminProjectQuery{OwnerID: scope.Actor, Limit: 1, Cursor: first.NextCursor})
	wantError(t, err, ErrInvalid)
	owner, err := s.AdminProjects(ctx, token, AdminProjectQuery{OwnerID: scope.Actor, Search: "Alpha"})
	must(t, err)
	if len(owner.Projects) != 1 || owner.Projects[0].ID != scope.ProjectID {
		t.Fatal(owner)
	}
	ordinary, err := s.Account(ctx, "bob")
	must(t, err)
	_, userToken, err := s.Authenticate(ctx, ordinary.Username, "fixture-password")
	must(t, err)
	_, err = s.AdminProject(ctx, userToken, scope.ProjectID, inspector)
	wantError(t, err, ErrForbidden)
	_, err = s.AdminProjects(ctx, userToken, AdminProjectQuery{})
	wantError(t, err, ErrForbidden)
	_, err = s.AdminProject(ctx, token, "missing", inspector)
	wantError(t, err, ErrNotFound)
}
