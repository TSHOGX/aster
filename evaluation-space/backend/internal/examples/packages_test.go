package examples

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/runner"
	"aster/evaluation-space/internal/workflow"
)

type exampleCatalog struct {
	graphs          map[evolution.Revision]evolution.Workflow
	schemas         map[string]json.RawMessage
	implementations map[evolution.Revision]runner.Transform
}

func (c exampleCatalog) ResolveWorkflow(_ context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	return c.graphs[ref], nil
}
func (c exampleCatalog) Schema(_ context.Context, digest string) (json.RawMessage, error) {
	return c.schemas[digest], nil
}
func (c exampleCatalog) Check(context.Context, evolution.Node) error { return nil }

type exampleRuntime struct {
	t       *testing.T
	catalog exampleCatalog
	critic  int
	alter   bool
}

func (r *exampleRuntime) Execute(ctx context.Context, call workflow.Call) (workflow.Values, error) {
	if call.Node.Kind != "agent" {
		return r.catalog.implementations[*call.Node.ImplementationRef](ctx, call)
	}
	items := []Proposal{{ID: "supported", Title: "Reusable lesson", Body: "Perform the verified operation and check the result.", EvidenceIDs: []string{"episode"}, Status: "supported"}, {ID: "unsupported", Title: "Unverified claim", Body: "Do not promote this claim.", EvidenceIDs: []string{"missing"}, Status: "supported"}, {ID: "failure", Title: "Failed operation", Body: "Requires investigation.", EvidenceIDs: []string{"failed"}, Status: "supported"}}
	switch call.Node.ID {
	case "extract":
		raw, _ := json.Marshal(items)
		return workflow.Values{"items": raw}, nil
	case "draft":
		corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(State{Corpus: corpus, Items: items, Valid: false})
		return workflow.Values{"state": raw}, nil
	case "critic":
		state, err := decodeInput[State](call.Inputs, "state")
		if err != nil {
			return nil, err
		}
		r.critic++
		state.Valid = r.critic == 2
		if r.alter {
			state.Corpus.Episodes[0].Text = "invented authoritative evidence"
		}
		raw, _ := json.Marshal(state)
		return workflow.Values{"state": raw}, nil
	}
	r.t.Fatal("unexpected agent", call.Node.ID)
	return nil, nil
}
func TestExamplePackagesExecuteDistinctGraphsAndPreserveHeldEvidence(t *testing.T) {
	model := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	packages, err := Packages(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		t.Run(pkg.Template.Ref.ID, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "space.db")
			store, err := projections.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			catalog := exampleCatalog{graphs: map[evolution.Revision]evolution.Workflow{}, schemas: pkg.Schemas, implementations: Implementations()}
			for _, published := range pkg.Workflows {
				catalog.graphs[published.Ref] = published.Workflow
				raw, err := published.Workflow.Canonical()
				if err != nil {
					t.Fatal(err)
				}
				if err = store.PublishWorkflow(ctx, published.Ref, raw); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(pkg.Template)
			if err = store.PublishDefinition(ctx, "template", raw); err != nil {
				t.Fatal(err)
			}
			run := evolution.Run{Pins: evolution.RunPins{Workflow: pkg.Template.Workflows[0], SourcePolicyRevision: 1}, ID: "run", GroupID: "group", Budget: evolution.Budget{MaxAttempts: 20, MaxTokens: 1000, MaxCostMicros: 100, MaxArtifactBytes: 1 << 20, TimeoutSeconds: 600}}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(run)
			_, err = db.Exec(`INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, body)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			lease, err := store.ClaimRun(ctx, "run", "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &exampleRuntime{t: t, catalog: catalog}
			engine := workflow.Engine{Catalog: catalog, Runtime: runtime, Journal: projections.WorkflowJournal{Store: store, Lease: lease, GrantID: "run-grant"}}
			corpus := Corpus{Episodes: []Episode{{ID: "episode", FamilyID: "incident", FamilyKind: "declared", Outcome: "completed", Text: "Verified operation completed."}, {ID: "failed", FamilyID: "incident", FamilyKind: "declared", Outcome: "failed", Text: "The retry failed."}}}
			input, _ := json.Marshal(corpus)
			values, err := engine.Execute(ctx, pkg.Template.Workflows[0], workflow.Values{"corpus": input})
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := evolution.Decode[Candidate](bytes.NewReader(values["candidate"]))
			if err != nil || len(candidate.Files) != 1 || len(candidate.Held) != 2 {
				t.Fatal(candidate, err)
			}
			if !strings.Contains(candidate.Files[0].Text, "Evidence IDs: episode") || strings.Contains(candidate.Files[0].Text, "Unverified") {
				t.Fatal(candidate)
			}
			if pkg.Template.SuggestedPrefix == "skills" {
				if runtime.critic != 2 || candidate.Files[0].Path != "supported/SKILL.md" {
					t.Fatal(runtime.critic, candidate)
				}
			} else if runtime.critic != 0 || candidate.Files[0].Path != "supported.md" {
				t.Fatal(candidate)
			}
			again, err := engine.Execute(ctx, pkg.Template.Workflows[0], workflow.Values{"corpus": input})
			if err != nil || !bytes.Equal(values["candidate"], again["candidate"]) {
				t.Fatal("example recovery changed output", err)
			}
		})
	}
}
func TestBuildersRejectForgedEvidenceAndUnsafeNames(t *testing.T) {
	corpus := Corpus{Episodes: []Episode{{ID: "episode", FamilyID: "family", FamilyKind: "declared", Outcome: "completed", Text: "original"}}}
	items := []Proposal{{ID: "safe", Title: "Title", Body: "Body", EvidenceIDs: []string{"episode"}, Status: "supported"}}
	corpusRaw, _ := json.Marshal(corpus)
	state := State{Corpus: corpus, Items: items, Valid: true}
	state.Corpus.Episodes = append([]Episode(nil), corpus.Episodes...)
	state.Corpus.Episodes[0].Text = "forged"
	stateRaw, _ := json.Marshal(state)
	if _, err := Implementations()[BuildSkills](context.Background(), workflow.Call{Inputs: workflow.Values{"corpus": corpusRaw, "state": stateRaw}}); err == nil {
		t.Fatal("critic changed original corpus")
	}
	for _, id := range []string{"../escape", "AGENTS.md", ".git", "nested/path", ""} {
		bad := append([]Proposal(nil), items...)
		bad[0].ID = id
		if _, err := verify(corpus, bad); err == nil {
			t.Fatal("unsafe proposal", id)
		}
	}
	corpus.Episodes[0].Truncated = true
	verified, err := verify(corpus, items)
	if err != nil || verified[0].Status != "held" {
		t.Fatal("truncated evidence promoted", verified, err)
	}
}

func TestEvidenceCopiesCannotInflateSupportAndUnknownFamiliesAreHeld(t *testing.T) {
	corpus := Corpus{Episodes: []Episode{{ID: "a", FamilyID: "origin", FamilyKind: "local", Outcome: "completed"}, {ID: "b", FamilyID: "origin", FamilyKind: "copy", Outcome: "completed"}, {ID: "unknown", FamilyID: "unknown", FamilyKind: "unknown", Outcome: "completed"}}}
	items := []Proposal{{ID: "known", Title: "Known", Body: "Verified", EvidenceIDs: []string{"b", "a"}, Status: "supported"}, {ID: "unknown", Title: "Unknown", Body: "Unverified origin", EvidenceIDs: []string{"unknown"}, Status: "supported"}}
	result, err := verify(corpus, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(result[0].EvidenceIDs) != 1 || result[0].EvidenceIDs[0] != "a" || result[1].Status != "held" {
		t.Fatal("family evidence inflated", result)
	}
}
