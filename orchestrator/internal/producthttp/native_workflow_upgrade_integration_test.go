//go:build integration

package producthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/evolutionspaceclient"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/studioclient"
)

type nativeWorkflowUpgrade struct {
	BrowserUpgrade                  bool
	Repository                      string
	Store                           *product.Store
	Token, Issuer, Actor, SourceRun string
	EvolutionSpace                  *evolutionspaceclient.Client
	Restart                         func()
	Studio                          *studioclient.Client
	Configuration                   evolution.Configuration
	Template                        evolution.Template
	Artifact                        evolution.ArtifactRef
	Browser                         func(string, string, any) (int, []byte)
	StudioBrowser                   func(string, string, any) (int, []byte)
	Private                         func(string, any) (int, []byte)
}

// Publication and activation use the actual browser authority, central operation
// worker and both participant processes. The model transport remains the native
// integration fixture; this helper does not simulate activation receipts.
func verifyNativeWorkflowUpgrade(t *testing.T, ctx context.Context, f nativeWorkflowUpgrade, base evolution.ContentRef) {
	t.Helper()
	call := func(method, path string, body any, code int, result any) {
		t.Helper()
		status, raw := f.Browser(method, path, body)
		if status != code {
			t.Fatalf("upgrade %s %s: %d %s", method, path, status, raw)
		}
		if result != nil {
			must(t, json.Unmarshal(raw, result))
		}
	}
	var original evolution.Run
	runRoot := "groups/" + f.Configuration.GroupID + "/runs/"
	call("GET", runRoot+f.SourceRun, nil, 200, &original)
	root := "groups/" + f.Configuration.GroupID + "/workflows"
	var draft struct {
		ID             string             `json:"id"`
		Revision       int64              `json:"revision"`
		LayoutRevision int64              `json:"layout_revision"`
		Workflow       evolution.Workflow `json:"workflow"`
	}
	call("POST", root, map[string]any{"id": "browser-upgrade-workflow", "source_run_id": f.SourceRun}, 201, &draft)
	root += "/" + draft.ID + "/draft"
	var v1 evolution.Revision
	call("POST", root+"/publish", map[string]int64{"revision": draft.Revision}, 201, &v1)
	if v1.Revision != 1 {
		t.Fatal("first author publication", v1)
	}
	publishTemplate := func(refs []evolution.Revision, revision int64) evolution.Template {
		value := f.Template
		value.Ref = evolution.Revision{ID: "browser-upgrade-template", Revision: revision}
		value.Workflows = refs
		var err error
		value.Ref.Digest, err = evolution.DefinitionDigest(value)
		must(t, err)
		var published evolution.Revision
		call("POST", "groups/"+f.Configuration.GroupID+"/templates", value, 201, &published)
		if published != value.Ref {
			t.Fatal("online template identity", published, value.Ref)
		}
		f.Restart()

		return value
	}
	template1 := publishTemplate([]evolution.Revision{v1}, 1)
	clone := func(value evolution.Configuration) evolution.Configuration {
		raw, err := json.Marshal(value)
		must(t, err)
		var copy evolution.Configuration
		must(t, json.Unmarshal(raw, &copy))
		return copy
	}
	selected := clone(f.Configuration)
	selected.Revision++
	for i := range selected.Bindings {
		selected.Bindings[i].Output.ReservationGeneration++
	}
	bindingIndex := -1
	for i := range selected.Bindings {
		if selected.Bindings[i].ID == "example-skills" {
			bindingIndex = i
		}
	}
	if bindingIndex < 0 {
		t.Fatal("missing skills binding")
	}
	selected.Bindings[bindingIndex].Workflow = v1
	selected.Bindings[bindingIndex].Template = template1.Ref
	activate := func(request string, config evolution.Configuration) product.EvolutionGroupConfig {
		result, err := f.Store.ActivateEvolutionConfig(ctx, f.Token, config.GroupID, product.EvolutionActivationInput{RequestID: request, ExpectedRevision: config.Revision - 1, Configuration: config})
		must(t, err)
		must(t, (&product.EvolutionWorker{Store: f.Store, EvolutionSpace: f.EvolutionSpace, Studio: f.Studio}).Step(ctx))
		active, err := f.Store.EvolutionConfig(ctx, f.Token, config.GroupID)
		must(t, err)
		if active.State != "active" || active.ActiveRevision != config.Revision || active.ActiveDigest != result.DesiredDigest {
			operation, _ := f.Store.EvolutionOperation(ctx, f.Token, config.GroupID, active.OperationID)
			t.Fatal("upgrade activation", active, operation)
		}
		return active
	}
	first := activate("authored-v1", selected)
	name := "Pinned workflow group"
	group, err := f.Store.SaveGroup(ctx, f.Token, "", product.GroupChange{RequestID: "pinned-workflow-group", Name: &name})
	must(t, err)
	second := clone(selected)
	second.GroupID = group.Group.ID
	second.Revision = 1
	second.Bindings = []evolution.Binding{second.Bindings[bindingIndex]}
	second.Bindings[0].Output.Prefix = "isolated-workflow"
	second.Bindings[0].Output.ReservationGeneration = 1
	status, raw := f.StudioBrowser("POST", "libraries", map[string]string{"request_id": "pinned-library", "name": "Pinned workflow output", "description": "Independent group output", "profile": "shared-assets-v1"})
	if status != 201 {
		t.Fatal("independent output volume", status, string(raw))
	}
	var library struct {
		ID string `json:"id"`
	}
	must(t, json.Unmarshal(raw, &library))
	second.Bindings[0].Output.LibraryID = library.ID
	for _, action := range []string{"volume.read", "volume.configure"} {
		_, err = f.Store.ApplyStudioGrant(ctx, f.Token, product.StudioGrantCommand{RequestID: "pinned-" + strings.ReplaceAll(action, ".", "-"), Kind: "account", SubjectID: f.Actor, LibraryID: library.ID, Action: action, Effect: "allow"})
		must(t, err)
	}
	for i := range second.Sources {
		second.Sources[i].AudienceGroups = []string{second.GroupID}
		_, err = f.Store.SetEvolutionSourcePolicy(ctx, f.Token, second.GroupID, product.EvolutionSourcePolicyInput{RequestID: "pinned-source", Source: second.Sources[i]})
		must(t, err)
	}
	other := activate("pinned-v1", second)
	binding := selected.Bindings[bindingIndex]
	pins := evolution.RunPins{ConfigurationRevision: selected.Revision, ConfigurationDigest: first.ActiveDigest, SourcePolicyRevision: 1, Template: binding.Template, Workflow: binding.Workflow, Service: binding.Service, InputPorts: map[string]evolution.ArtifactRef{"corpus": f.Artifact}, Inputs: []evolution.ArtifactRef{f.Artifact}, Base: base, Output: binding.Output, Evaluation: binding.Evaluation}
	assignment := evolution.GenerationAssignment{RunID: "browser-upgrade-pinned-run", GroupID: selected.GroupID, BindingID: binding.ID, OccurrenceID: "manual-upgrade", Requester: evolution.Principal{Issuer: f.Issuer, Subject: f.Actor, Kind: "human"}, Pins: pins, Budget: binding.Budget}
	status, raw = f.Private("admit", assignment)
	if status != 200 {
		t.Fatal("upgrade run admission", status, string(raw))
	}
	var frozen evolution.Run
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		call("GET", runRoot+assignment.RunID, nil, 200, &frozen)
		if frozen.State == "failed" || frozen.State == "completed" && (frozen.Handoff == "submitted" || frozen.Handoff == "no_change") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if frozen.State != "completed" || frozen.Pins.Workflow != v1 {
		t.Fatal("authored v1 execution", frozen)
	}
	var v2 evolution.Revision
	var template2 evolution.Template

	if f.BrowserUpgrade {
		runNativeEvolutionBrowser(t, ctx, f, "publish", assignment.RunID)
		var catalog struct {
			Revisions []evolution.Revision `json:"revisions"`
		}
		call("GET", "groups/"+selected.GroupID+"/templates", nil, 200, &catalog)
		for _, ref := range catalog.Revisions {
			if ref.ID == template1.Ref.ID && ref.Revision == 2 {
				call("GET", "groups/"+selected.GroupID+"/templates/browser-upgrade-template/2?digest="+ref.Digest, nil, 200, &template2)
			}
		}
		for _, ref := range template2.Workflows {
			if ref.ID == v1.ID && ref.Revision == 2 {
				v2 = ref
			}
		}
		if v2.ID == "" || v2.Digest == v1.Digest {
			t.Fatal("browser did not publish new workflow", template2)
		}
	} else {
		// Publish v2 without altering either group's current configuration.
		for i := range draft.Workflow.Nodes {
			if draft.Workflow.Nodes[i].Prompt != nil {
				prompt := draft.Workflow.Nodes[i].Prompt
				prompt.Text += "\nKeep explicit source evidence."
				prompt.Digest = evolution.Digest([]byte(prompt.Text))
				break
			}
		}
		call("PUT", root, map[string]any{"id": draft.ID, "expected_revision": draft.Revision, "expected_layout_revision": draft.LayoutRevision, "workflow": draft.Workflow, "layout": map[string]any{}}, 200, &draft)
		call("POST", root+"/publish", map[string]int64{"revision": draft.Revision}, 201, &v2)
		if v2.ID != v1.ID || v2.Revision != 2 || v2.Digest == v1.Digest {
			t.Fatal("immutable author revision", v1, v2)
		}
		template2 = publishTemplate([]evolution.Revision{v1, v2}, 2)
	}
	for _, prior := range []product.EvolutionGroupConfig{first, other} {
		current, err := f.Store.EvolutionConfig(ctx, f.Token, prior.GroupID)
		must(t, err)
		if current != prior {
			t.Fatal("publication activated a group", prior, current)
		}
	}
	selected.Revision++
	for i := range selected.Bindings {
		selected.Bindings[i].Output.ReservationGeneration++
	}
	selected.Bindings[bindingIndex].Workflow = v2
	selected.Bindings[bindingIndex].Template = template2.Ref
	var upgraded product.EvolutionGroupConfig
	if f.BrowserUpgrade {
		runNativeEvolutionBrowser(t, ctx, f, "activate", assignment.RunID)
		upgraded, err = f.Store.EvolutionConfig(ctx, f.Token, selected.GroupID)
		must(t, err)
		if upgraded.State != "active" || upgraded.ActiveRevision != selected.Revision {
			t.Fatal("browser activation", upgraded)
		}
		actual, err := f.Store.EvolutionConfigurationRevision(ctx, f.Token, selected.GroupID, upgraded.ActiveRevision)
		must(t, err)
		want, _ := json.Marshal(selected)
		got, _ := json.Marshal(actual.Configuration)
		if !bytes.Equal(want, got) {
			t.Fatalf("browser changed unrelated configuration\nwant %s\ngot %s", want, got)
		}
	} else {
		upgraded = activate("authored-v2", selected)
	}
	currentOther, err := f.Store.EvolutionConfig(ctx, f.Token, second.GroupID)
	must(t, err)
	if currentOther != other {
		t.Fatal("selected activation changed other group", currentOther)
	}
	for _, check := range []struct {
		group    string
		revision int64
		workflow evolution.Revision
	}{{selected.GroupID, upgraded.ActiveRevision, v2}, {second.GroupID, other.ActiveRevision, v1}} {
		retained, err := f.Store.EvolutionConfigurationRevision(ctx, f.Token, check.group, check.revision)
		must(t, err)
		found := false
		for _, binding := range retained.Configuration.Bindings {
			if binding.ID == "example-skills" {
				found = true
				if binding.Workflow != check.workflow {
					t.Fatal("group workflow drift", binding.Workflow, check.workflow)
				}
			}
		}
		if !found {
			t.Fatal("missing retained binding")
		}
	}
	var after evolution.Run
	call("GET", runRoot+assignment.RunID, nil, 200, &after)
	beforePins, _ := json.Marshal(frozen.Pins)
	afterPins, _ := json.Marshal(after.Pins)
	if !bytes.Equal(beforePins, afterPins) {
		t.Fatal("activation changed admitted run pins")
	}
	call("GET", runRoot+f.SourceRun, nil, 200, &after)
	beforePins, _ = json.Marshal(original.Pins)
	afterPins, _ = json.Marshal(after.Pins)
	if !bytes.Equal(beforePins, afterPins) {
		t.Fatal("activation changed original run pins")
	}
	var retained struct {
		Workflow evolution.Workflow `json:"workflow"`
	}
	call("GET", runRoot+assignment.RunID+"/workflow", nil, 200, &retained)
	canonical, err := retained.Workflow.Canonical()
	must(t, err)
	if evolution.Digest(canonical) != v1.Digest {
		t.Fatal("existing run resolved latest workflow")
	}
	assignment.RunID = "browser-upgrade-new-run"
	assignment.OccurrenceID = "manual-after-upgrade"
	assignment.Pins.ConfigurationRevision = selected.Revision
	assignment.Pins.ConfigurationDigest = upgraded.ActiveDigest
	assignment.Pins.Workflow = v2
	assignment.Pins.Template = template2.Ref
	assignment.Pins.Output = selected.Bindings[bindingIndex].Output
	// Terminal accounting is reconciled asynchronously; allow its capacity
	// release to become visible while replaying the same admission identity.
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		status, raw = f.Private("admit", assignment)
		if status == 200 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status != 200 {
		t.Fatal("v2 admission", status, string(raw))
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		call("GET", runRoot+assignment.RunID, nil, 200, &after)
		if after.State == "completed" || after.State == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if after.State != "completed" || after.Pins.Workflow != v2 {
		t.Fatal("new run did not execute v2", after)
	}
}

func runNativeEvolutionBrowser(t *testing.T, ctx context.Context, f nativeWorkflowUpgrade, phase, sourceRun string) []byte {
	t.Helper()
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker := &product.EvolutionWorker{Store: f.Store, EvolutionSpace: f.EvolutionSpace, Studio: f.Studio}
		for {
			if err := worker.Step(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
				t.Log("upgrade activation pending", err)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	cmd := exec.CommandContext(ctx, "node", "node_modules/@playwright/test/cli.js", "test", "--config", "playwright.evolution.config.ts")
	cmd.Dir = filepath.Join(f.Repository, "admin-frontend")
	cmd.Env = append(os.Environ(), "ADMIN_EVOLUTION_URL="+f.Issuer, "ADMIN_EVOLUTION_TOKEN="+f.Token, "ADMIN_EVOLUTION_UPGRADE_PHASE="+phase, "ADMIN_EVOLUTION_SOURCE_RUN="+sourceRun)
	output, err := cmd.CombinedOutput()
	stop()
	<-done
	if err != nil {
		t.Fatalf("upgrade browser %s: %v\n%s", phase, err, output)
	}
	return output
}
