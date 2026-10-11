package product

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func TestOperatorPlatformLimitsFenceReceiptsAndRetainSavedRequests(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	s := l.Store
	token := suspensionAdmin(t, s)
	before, err := s.UserResourceSettings(ctx, token, scope.Actor)
	must(t, err)
	workspace := before.Effective[ResourceWorkspace].Effective
	value := workspace
	applyResourceFixture(t, s, token, ResourceChange{Kind: "user", TargetID: scope.Actor, ExpectedRevision: before.Revision, Overrides: ResourceOverrides{ResourceWorkspace: &value}})
	must(t, l.EnsureActive(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	resources, err := s.Resources(ctx, scope)
	must(t, err)
	limits, err := s.OperatorPlatformLimits(ctx)
	must(t, err)
	if len(limits) != len(resourceDimensions) {
		t.Fatal(limits)
	}
	input := PlatformLimitEdit{Dimension: ResourceWorkspace, Maximum: 0, ExpectedRevision: 1, RequestID: "zero-workspace-cap"}
	_, err = s.db.Exec(`CREATE TRIGGER limit_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='platform.limit.update' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	must(t, err)
	if _, err = s.EditPlatformLimit(ctx, input); err == nil {
		t.Fatal("audit failure ignored")
	}
	unchanged, err := s.UserResourceSettings(ctx, token, scope.Actor)
	must(t, err)
	if unchanged.Effective[ResourceWorkspace].Effective != workspace {
		t.Fatal(unchanged)
	}
	_, err = s.db.Exec(`DROP TRIGGER limit_audit_fault`)
	must(t, err)
	changed, err := s.EditPlatformLimit(ctx, input)
	must(t, err)
	if changed.Maximum != 0 || changed.Revision != 2 {
		t.Fatal(changed)
	}
	replay, err := s.EditPlatformLimit(ctx, input)
	must(t, err)
	if !replay.Replayed || replay.Revision != changed.Revision {
		t.Fatal(replay)
	}
	settings, err := s.UserResourceSettings(ctx, token, scope.Actor)
	must(t, err)
	if settings.Effective[ResourceWorkspace].Effective != 0 || !settings.Effective[ResourceWorkspace].Capped || *settings.Overrides[ResourceWorkspace] != workspace || settings.AuthorizationRevision <= before.AuthorizationRevision {
		t.Fatal(settings)
	}
	retained, err := s.Resources(ctx, scope)
	must(t, err)
	if retained.Profile.CapacityForRole("workspace") != resources.Profile.CapacityForRole("workspace") || retained.VolumeForRole("workspace") != resources.VolumeForRole("workspace") {
		t.Fatal("cap resized existing allocation", retained)
	}
	pausePolicyProject(t, l, scope)
	wantError(t, l.Resume(ctx, scope, testProfile().Bindings()), ErrQuotaExceeded)
	input.RequestID = "restore-cap"
	input.Maximum = workspace
	input.ExpectedRevision = 2
	_, err = s.EditPlatformLimit(ctx, input)
	must(t, err)
	settings, err = s.UserResourceSettings(ctx, token, scope.Actor)
	must(t, err)
	if settings.Effective[ResourceWorkspace].Effective != workspace || settings.Effective[ResourceWorkspace].Capped {
		t.Fatal(settings)
	}
	must(t, l.Resume(ctx, scope, testProfile().Bindings()))
	drive(t, l, scope.ProjectID)
	input.Maximum++
	_, err = s.EditPlatformLimit(ctx, input)
	wantError(t, err, ErrConflict)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE application_release SET phase='draining'`)
		return err
	}))
	input.RequestID = "fenced-cap"
	input.ExpectedRevision = 3
	_, err = s.EditPlatformLimit(ctx, input)
	wantError(t, err, ErrDraining)
	input = PlatformLimitEdit{Dimension: ResourceWorkspace, Maximum: 0, ExpectedRevision: 1, RequestID: "zero-workspace-cap"}
	replay, err = s.EditPlatformLimit(ctx, input)
	must(t, err)
	if !replay.Replayed {
		t.Fatal(replay)
	}
}

func TestOperatorLimitInputIsExactAndBounded(t *testing.T) {
	s := testStore(t)
	for _, input := range []PlatformLimitEdit{
		{Dimension: "unknown", Maximum: 1, ExpectedRevision: 1, RequestID: "unknown"},
		{Dimension: ResourceMounts, Maximum: 0, ExpectedRevision: 1, RequestID: "mount-zero"},
		{Dimension: ResourceMounts, Maximum: 129, ExpectedRevision: 1, RequestID: "mount-high"},
		{Dimension: ResourceTransfer, Maximum: 256<<20 + 1, ExpectedRevision: 1, RequestID: "transfer-high"},
	} {
		_, err := s.EditPlatformLimit(ctx, input)
		wantError(t, err, ErrInvalid)
	}
	for _, raw := range []string{
		`{"dimension":"workspace_bytes","expected_revision":1,"request_id":"missing"}`,
		`{"dimension":"workspace_bytes","maximum":null,"expected_revision":1,"request_id":"null"}`,
		`{"dimension":"workspace_bytes","maximum":1,"expected_revision":1,"request_id":"number"}`,
		`{"dimension":"workspace_bytes","maximum":"01","expected_revision":1,"request_id":"leading"}`,
		`{"dimension":"workspace_bytes","maximum":"1","expected_revision":1,"request_id":"extra","unreviewed":true}`,
	} {
		var input PlatformLimitEdit
		if json.Unmarshal([]byte(raw), &input) == nil {
			t.Fatal("incomplete or rounded cap input accepted", raw)
		}
	}
}
