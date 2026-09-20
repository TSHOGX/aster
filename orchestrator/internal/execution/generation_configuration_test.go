package execution_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

// mutablePlan lets a test change one generation's declaration the way publishing
// a new content revision or reserving a new service destination would.
type mutablePlan struct{ plan execution.LaunchPlan }

func (p *mutablePlan) Plan(context.Context, execution.Session, string) (execution.LaunchPlan, error) {
	return p.plan, nil
}

func configurationFixture(t *testing.T) (*execution.Controller, *store.Store, *controllerBackend, *scriptedLauncher, *mutablePlan, string) {
	t.Helper()
	controller, st, backend, launcher, owner := controllerFixture(t)
	plans := &mutablePlan{plan: execution.LaunchPlan{
		TemplateID: "template", SpecDigest: "sha256:" + strings.Repeat("a", 64), ManifestDigest: "sha256:" + strings.Repeat("b", 64),
		CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, WorkspaceBytes: 64 << 20, JournalBytes: 64 << 20,
		Mounts: []sandbox.Mount{{VolumeID: "assets-rev1", Destination: "/root/.agents", Access: "shared-ro"}},
	}}
	controller.Plans = plans
	controller.Routes = &recordingRevoker{}
	return controller, st, backend, launcher, plans, owner
}

func TestRetainedEnvironmentAdoptsNewContentRevision(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, plans, owner := configurationFixture(t)
	session := execution.Session{ID: "session", Driver: "sh"}
	first, err := controller.Ensure(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := st.GetEnvironmentBinding(ctx, first.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	applied, present, err := environment.AppliedConfiguration()
	if err != nil || !present {
		t.Fatalf("create did not record its applied declaration: %v %v", present, err)
	}
	if len(applied.Mounts) == 0 {
		t.Fatalf("applied declaration has no mounts: %+v", applied)
	}
	found := false
	for _, mount := range applied.Mounts {
		if mount.VolumeID == "assets-rev1" && mount.Destination == "/root/.agents" && mount.Access == "shared-ro" {
			found = true
		}
	}
	if !found {
		t.Fatalf("published content mount missing from applied declaration: %+v", applied.Mounts)
	}

	// Release the execution so the retained environment is paused, which is the
	// only state the engine accepts a declaration change in.
	if err := controller.ReleaseIdle(ctx, session.ID, first.GenerationID, owner); err != nil {
		t.Fatal(err)
	}

	// Publishing a newer asset revision changes the generation declaration. A
	// retained environment must adopt it rather than conflicting forever.
	plans.plan.Mounts = []sandbox.Mount{{VolumeID: "assets-rev2", Destination: "/root/.agents", Access: "shared-ro"}}
	plans.plan.SpecDigest = "sha256:" + strings.Repeat("c", 64)
	second, err := controller.Ensure(ctx, session, owner)
	if err != nil {
		t.Fatalf("retained environment refused a newly published revision: %v", err)
	}
	if second.EnvironmentID != first.EnvironmentID || second.SandboxID != first.SandboxID {
		t.Fatalf("adoption replaced the retained environment: %+v -> %+v", first, second)
	}
	if len(backend.createKeys) != 1 {
		t.Fatalf("adoption created a second sandbox: %v", backend.createKeys)
	}
	environment, err = st.GetEnvironmentBinding(ctx, second.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	applied, present, err = environment.AppliedConfiguration()
	if err != nil || !present {
		t.Fatal("configure did not record its applied declaration")
	}
	if applied.SpecDigest != plans.plan.SpecDigest {
		t.Fatalf("applied declaration is not the new generation's: %+v", applied)
	}
	for _, mount := range applied.Mounts {
		if mount.VolumeID == "assets-rev1" {
			t.Fatalf("superseded content revision is still declared: %+v", applied.Mounts)
		}
	}
	if !backend.configured {
		t.Fatal("no configure operation reached the engine")
	}
}

func TestUnchangedConfigurationCostsNoEngineMutation(t *testing.T) {
	ctx := context.Background()
	controller, st, backend, _, _, owner := configurationFixture(t)
	session := execution.Session{ID: "session", Driver: "sh"}
	binding, err := controller.Ensure(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReleaseIdle(ctx, session.ID, binding.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	backend.configured = false
	if _, err := controller.Ensure(ctx, session, owner); err != nil {
		t.Fatal(err)
	}
	if backend.configured {
		t.Fatal("an unchanged declaration was reapplied to the engine")
	}
	environment, err := st.GetEnvironmentBinding(ctx, binding.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present, err := environment.AppliedConfiguration(); err != nil || !present {
		t.Fatal("applied declaration was lost across resume")
	}
}

func TestEnvironmentIdentityStillConflictsExactly(t *testing.T) {
	for _, change := range []string{"template", "manifest", "memory", "workspace"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			controller, _, _, _, plans, owner := configurationFixture(t)
			session := execution.Session{ID: "session", Driver: "sh"}
			binding, err := controller.Ensure(ctx, session, owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := controller.ReleaseIdle(ctx, session.ID, binding.GenerationID, owner); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "template":
				plans.plan.TemplateID = "different-template"
			case "manifest":
				plans.plan.ManifestDigest = "sha256:" + strings.Repeat("e", 64)
			case "memory":
				plans.plan.MemoryBytes = 2 << 30
			case "workspace":
				plans.plan.WorkspaceBytes = 128 << 20
			}
			if _, err := controller.Ensure(ctx, session, owner); !errors.Is(err, execution.ErrOperationConflict) {
				t.Fatalf("changed environment identity was silently adopted: %v", err)
			}
		})
	}
}

func TestGenerationConfigurationRefusesRunningEnvironment(t *testing.T) {
	ctx := context.Background()
	controller, st, _, _, plans, owner := configurationFixture(t)
	session := execution.Session{ID: "session", Driver: "sh"}
	binding, err := controller.Ensure(ctx, session, owner)
	if err != nil {
		t.Fatal(err)
	}
	plans.plan.Mounts = []sandbox.Mount{{VolumeID: "assets-rev2", Destination: "/root/.agents", Access: "shared-ro"}}
	plans.plan.SpecDigest = "sha256:" + strings.Repeat("c", 64)
	// A live execution's declaration cannot change underneath it. Reserving the
	// intent directly must be refused while its binding is not retired.
	if _, err := st.BeginGenerationConfiguration(ctx, binding.EnvironmentID, owner, execution.GenerationConfiguration{SpecDigest: plans.plan.SpecDigest, Mounts: plans.plan.Mounts}); err == nil {
		t.Fatal("reconfiguration was reserved while an execution was accepted")
	}
}
