//go:build khoraintegration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
)

func qualifyImportedProjectAdoption(t *testing.T, ctx context.Context, app *Application, token, actor string, imported product.VolumeImport) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := app.publish(ctx, app.Config.Content)
	must(err)
	lifecycle := &product.Lifecycle{Store: app.Store, Backend: app.Backend, VerifyRuntime: app.Backend.VerifyRuntime, MaxProjects: 3}
	scopes := []product.Scope{{Actor: actor, ProjectID: "selected"}, {Actor: actor, ProjectID: "unselected"}}
	converge := func(scope product.Scope) {
		t.Helper()
		for n := 0; n < 60; n++ {
			err := lifecycle.Reconcile(ctx, scope.ProjectID)
			if errors.Is(err, product.ErrNotFound) {
				return
			}
			must(err)
		}
		t.Fatal("Project lifecycle did not converge")
	}
	command := func(scope product.Scope, handle, script string) string {
		t.Helper()
		resources, err := lifecycle.ActiveResources(ctx, scope)
		must(err)
		stream, err := app.Backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: resources.Reference.StoreID, SandboxID: resources.Reference.SandboxID, Epoch: resources.Reference.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", script}, Directory: "/workspace"})
		must(err)
		defer stream.Close()
		var output strings.Builder
		for {
			frame, err := stream.Next()
			must(err)
			if frame.Kind == "data" {
				output.Write(frame.Data)
			}
			if frame.Kind == "end" {
				if frame.ExitCode != 0 {
					t.Fatal(handle, frame.ExitCode, output.String())
				}
				return output.String()
			}
		}
	}
	for _, scope := range scopes {
		_, err = app.Store.CreateProject(ctx, scope.Actor, scope.ProjectID, "Explicit imported context adoption")
		must(err)
		must(lifecycle.EnsureActive(ctx, scope, app.Config.RuntimeBindings()))
		converge(scope)
		command(scope, "initial-"+scope.ProjectID, `set -eu; test "$(cat /opt/shared-assets/AGENTS.md)" = 'Initial guidance'; printf retained > user-file`)
	}
	before, err := app.Store.Resources(ctx, scopes[0])
	must(err)
	templates, err := app.Store.ResourceTemplates(ctx, token, "environment")
	must(err)
	var template product.ResourceTemplate
	for _, item := range templates {
		if item.ID == "standard-agent" {
			template = item
		}
	}
	if template.Environment == nil {
		t.Fatal("bootstrap environment missing")
	}
	next := *template.Environment
	next.Mounts = append([]product.EnvironmentMount(nil), next.Mounts...)
	for i, mount := range next.Mounts {
		if mount.Role == "shared_assets" {
			next.Mounts[i].Source = product.MountSource{Kind: "builtin", ArtifactID: imported.Artifact.ID, Version: imported.Artifact.Version, Digest: imported.Artifact.Digest}
		}
	}
	edit := product.TemplateEdit{RequestID: "select-imported-context", Preview: true, ExpectedRevision: template.Revision, Name: template.Name, Enabled: true, Environment: &next}
	raw, err := app.Store.SaveResourceTemplate(ctx, token, "environment", template.ID, edit)
	must(err)
	var impact product.ConfigurationImpact
	must(json.Unmarshal(raw, &impact))
	edit.Preview = false
	edit.ExpectedDependencies = impact.ExpectedDependencies
	_, err = app.Store.SaveResourceTemplate(ctx, token, "environment", template.ID, edit)
	must(err)
	templates, err = app.Store.ResourceTemplates(ctx, token, "environment")
	must(err)
	for _, item := range templates {
		if item.ID == template.ID {
			template = item
		}
	}
	// Import readiness and selecting a future default must not mutate either
	// already allocated Project, even though both accounts can use the content.
	for _, scope := range scopes {
		command(scope, "default-only-"+scope.ProjectID, `test "$(cat /opt/shared-assets/AGENTS.md)" = 'Initial guidance'`)
	}
	update := product.EnvironmentUpdateEdit{Kind: "system_content_replace", RequestID: "selected-context-rollout", Action: "publish", ExpectedRevision: template.Revision, Preview: true, ProjectIDs: []string{scopes[0].ProjectID}}
	raw, err = app.Store.ChangeEnvironmentUpdate(ctx, token, template.ID, update)
	must(err)
	must(json.Unmarshal(raw, &impact))
	update.Preview = false
	update.ExpectedDependencies = impact.ExpectedDependencies
	receipt, err := app.Store.ChangeEnvironmentUpdate(ctx, token, template.ID, update)
	must(err)
	replay, err := app.Store.ChangeEnvironmentUpdate(ctx, token, template.ID, update)
	must(err)
	if string(receipt) != string(replay) {
		t.Fatal("rollout receipt replay changed")
	}
	intended, err := app.Store.UserEnvironmentUpdate(ctx, token, scopes[0].ProjectID)
	must(err)
	if intended.State != "pending" || intended.TargetID == "" || intended.TargetID == intended.AppliedID {
		t.Fatal("missing delayed adoption", intended)
	}
	unaffected, err := app.Store.UserEnvironmentUpdate(ctx, token, scopes[1].ProjectID)
	must(err)
	if unaffected.TargetID != "" {
		t.Fatal("unselected Project targeted", unaffected)
	}
	command(scopes[0], "published-still-running", `test "$(cat /opt/shared-assets/AGENTS.md)" = 'Initial guidance'`)
	project, err := app.Store.Project(ctx, scopes[0])
	must(err)
	must(lifecycle.Pause(ctx, scopes[0], project.Revision))
	converge(scopes[0])
	must(lifecycle.Resume(ctx, scopes[0], app.Config.RuntimeBindings()))
	converge(scopes[0])
	command(scopes[0], "observed-import", `set -eu; test "$(cat /opt/shared-assets/AGENTS.md)" = 'Shared adoption qualification'; test "$(cat user-file)" = retained; test -f /opt/shared-assets/data-0.bin; if sh -c 'echo bad > /opt/shared-assets/AGENTS.md' 2>/dev/null; then exit 42; fi`)
	command(scopes[1], "unselected-stays-original", `test "$(cat /opt/shared-assets/AGENTS.md)" = 'Initial guidance'`)
	after, err := app.Store.Resources(ctx, scopes[0])
	must(err)
	observed, err := app.Store.UserEnvironmentUpdate(ctx, token, scopes[0].ProjectID)
	must(err)
	if observed.State != "current" || observed.AppliedID != intended.TargetID || after.VolumeForRole("workspace") != before.VolumeForRole("workspace") || after.VolumeForRole("runtime") != before.VolumeForRole("runtime") {
		t.Fatal("observed adoption lost identity or owned volumes", observed)
	}
	// The imported immutable source remains protected by selected mounts and the
	// retained rollout, even after the publisher is offline.
	if _, err = app.Store.RetireImportedVolume(ctx, token, imported.ID, "retire-mounted"); !errors.Is(err, product.ErrConflict) {
		t.Fatal("mounted import retired", err)
	}
	for _, scope := range scopes {
		p, err := app.Store.Project(ctx, scope)
		must(err)
		must(app.Store.DeleteProject(ctx, scope, p.Revision))
		converge(scope)
	}
	must(app.Store.ValidateReleasedBindings(ctx, imported.StoreID))
	t.Log("actual Studio branch/review/release -> offline import -> explicit selected shared-assets rollout -> delayed Project resume; unselected Project and owned user files preserved")
}
