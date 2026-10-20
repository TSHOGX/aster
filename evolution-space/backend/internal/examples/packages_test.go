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
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/workflow"
)

type exampleCatalog struct {
	graphs          map[evolution.Revision]evolution.Workflow
	schemas         map[string]json.RawMessage
	implementations map[evolution.Revision]Transform
}

func (c exampleCatalog) ResolveWorkflow(_ context.Context, ref evolution.Revision) (evolution.Workflow, error) {
	return c.graphs[ref], nil
}
func (c exampleCatalog) Schema(_ context.Context, digest string) (json.RawMessage, error) {
	return c.schemas[digest], nil
}
func (c exampleCatalog) Check(_ context.Context, node evolution.Node) error {
	if node.ImplementationRef != nil && c.implementations[*node.ImplementationRef] == nil {
		return workflow.Failure{Code: "unregistered"}
	}
	return nil
}

// The fixture corpus covers each evidence condition: a completed episode and its
// copy (one family), a failure, truncated capture, unknown provenance, a second
// failure, and an episode whose case record cannot be written.
var fixture = Corpus{Episodes: []Episode{
	{ID: "a-done", FamilyID: "deploy", FamilyKind: "declared", Outcome: "completed", Text: "Deployed with a health check; the check passed."},
	{ID: "b-copy", FamilyID: "deploy", FamilyKind: "copy", Outcome: "completed", Text: "Deployed with a health check; the check passed."},
	{ID: "c-failed", FamilyID: "migration", FamilyKind: "local", Outcome: "failed", Text: "The migration failed because the backup was skipped."},
	{ID: "d-trunc", FamilyID: "partial", FamilyKind: "local", Outcome: "completed", Truncated: true, Text: "cut"},
	{ID: "e-unknown", FamilyID: "unknown", FamilyKind: "unknown", Outcome: "completed", Text: "Imported without origin."},
	{ID: "f-failed", FamilyID: "report", FamilyKind: "local", Outcome: "failed", Text: "The report query timed out."},
	{ID: "g-broken", FamilyID: "broken", FamilyKind: "local", Outcome: "completed", Text: "Unreadable."},
}}

func p(id string, evidence []string, attributes map[string]string) Proposal {
	return Proposal{ID: id, Title: "Title " + id, Body: "Body for " + id + ".", EvidenceIDs: evidence, Status: "supported", Attributes: attributes}
}

var skillAttributes = map[string]string{"when_to_use": "Deploying a service", "verification": "The health check returns 200."}
var correctionAttributes = map[string]string{"scope": "migration", "trigger": "Running a migration", "mistaken_assumption": "A backup is optional.", "corrective_action": "Take a backup first.", "expected_result": "The migration can be rolled back."}

type exampleRuntime struct {
	t        *testing.T
	catalog  exampleCatalog
	critic   int
	alter    bool
	attempts map[string]int
}

func (r *exampleRuntime) Execute(ctx context.Context, call workflow.Call) (workflow.Values, error) {
	if call.Node.Kind != "agent" {
		return r.catalog.implementations[*call.Node.ImplementationRef](ctx, call)
	}
	r.attempts[call.Workflow.ID+"/"+call.Node.ID]++
	if !strings.Contains(call.Prompt, "untrusted data") {
		r.t.Error("agent prompt lacks grounding", call.Node.ID)
	}
	items := func(values ...Proposal) (workflow.Values, error) { return output("items", values) }
	switch call.Workflow.ID + "/" + call.Node.ID {
	case "example-skills/draft":
		corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
		if err != nil {
			return nil, err
		}
		secret := p("leaky", []string{"a-done"}, skillAttributes)
		secret.Body = "Run deploy with password: hunter2hunter2."
		return output("state", State{Corpus: corpus, Valid: false, Items: []Proposal{
			p("deploy-service", []string{"b-copy", "a-done"}, skillAttributes),
			p("blind-retry", []string{"c-failed"}, skillAttributes),
			secret,
		}})
	case "example-skill-critique/critic":
		state, err := decodeInput[State](call.Inputs, "state")
		if err != nil {
			return nil, err
		}
		r.critic++
		state.Valid = r.critic == 2
		if r.alter {
			state.Corpus.Episodes[0].Text = "invented authoritative evidence"
		}
		return output("state", state)
	case "example-corrections/extract":
		return items(p("backup-first", []string{"c-failed", "a-done"}, correctionAttributes), p("timeout", []string{"f-failed"}, correctionAttributes))
	case "example-facts/extract":
		return items(
			p("deploy-region", []string{"a-done"}, map[string]string{"kind": "fact", "scope": "deploy", "valid_from": "2026-10-01", "supersedes": "old-region"}),
			p("rumor", []string{"a-done"}, map[string]string{"kind": "rumor", "scope": "deploy"}),
			p("self", []string{"a-done"}, map[string]string{"kind": "decision", "scope": "deploy", "supersedes": "self"}),
		)
	case "example-knowledge/extract":
		return items(
			p("active-users", []string{"a-done"}, map[string]string{"kind": "metric", "source": "analytics.events", "entity": "user", "unit": "users", "grain": "day", "formula": "count(distinct user_id)"}),
			p("revenue", []string{"a-done"}, map[string]string{"kind": "metric", "source": "finance", "entity": "order", "formula": "sum(amount)"}),
			p("user", []string{"a-done"}, map[string]string{"kind": "entity", "source": "crm.users"}),
		)
	case "example-knowledge/consolidate":
		values, err := decodeInput[[]Proposal](call.Inputs, "items")
		if err != nil {
			return nil, err
		}
		for i := range values {
			if values[i].ID == "user" {
				values[i].Status, values[i].HoldReason = "supported", ""
				values[i].EvidenceIDs = append(values[i].EvidenceIDs, "d-trunc")
			}
		}
		return items(values...)
	case "example-episode-record/record":
		corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
		if err != nil || len(corpus.Episodes) != 1 {
			r.t.Fatal("record received more than one episode", err)
		}
		episode := corpus.Episodes[0]
		if episode.ID == "g-broken" {
			return nil, workflow.Failure{Code: "model_refused"}
		}
		outcome := episode.Outcome
		if episode.ID == "f-failed" {
			outcome = "completed"
		}
		return items(p("case", []string{episode.ID}, map[string]string{"outcome": outcome, "situation": "Context.", "actions": "Steps."}))
	}
	r.t.Fatal("unexpected agent", call.Workflow.ID, call.Node.ID)
	return nil, nil
}

type expectation struct {
	files []string
	held  map[string]string
}

var expected = map[string]expectation{
	"example-skills":      {[]string{"deploy-service/SKILL.md"}, map[string]string{"blind-retry": "unsuccessful_evidence", "leaky": "sensitive_content"}},
	"example-corrections": {[]string{"backup-first.md"}, map[string]string{"timeout": "unresolved"}},
	"example-facts":       {[]string{"deploy-region.md"}, map[string]string{"rumor": "invalid_field", "self": "invalid_supersedes"}},
	"example-knowledge":   {[]string{"metrics/active-users.md"}, map[string]string{"revenue": "incomplete_metric", "user": "truncated_evidence"}},
	"example-episodes":    {[]string{"case.md", "case-2.md"}, map[string]string{"case-3": "outcome_mismatch"}},
}

func execute(t *testing.T, pkg Package, runtime *exampleRuntime, corpus Corpus) (workflow.Values, func() workflow.Values) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runtime.catalog = exampleCatalog{graphs: map[evolution.Revision]evolution.Workflow{}, schemas: pkg.Schemas, implementations: Implementations()}
	for _, published := range pkg.Workflows {
		runtime.catalog.graphs[published.Ref] = published.Workflow
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
	run := evolution.Run{Pins: evolution.RunPins{Workflow: pkg.Template.Workflows[0], SourcePolicyRevision: 1}, ID: "run", GroupID: "group", Budget: evolution.Budget{MaxAttempts: 200, MaxTokens: 1000, MaxCostMicros: 100, MaxArtifactBytes: 4 << 20, TimeoutSeconds: 600}}
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
	engine := workflow.Engine{Catalog: runtime.catalog, Runtime: runtime, Journal: projections.WorkflowJournal{Store: store, Lease: lease, GrantID: "run-grant"}}
	input, _ := json.Marshal(corpus)
	run1 := func() workflow.Values {
		values, err := engine.Execute(ctx, pkg.Template.Workflows[0], workflow.Values{"corpus": input})
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	return run1(), run1
}

func TestStarterPackagesExecuteEachMemoryClassAndHoldWithReasons(t *testing.T) {
	model := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	packages, err := Packages(model)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != len(expected) {
		t.Fatal("unexpected starter catalog", len(packages))
	}
	for _, pkg := range packages {
		t.Run(pkg.Template.Ref.ID, func(t *testing.T) {
			runtime := &exampleRuntime{t: t, attempts: map[string]int{}}
			values, again := execute(t, pkg, runtime, fixture)
			result, err := evolution.Decode[Candidate](bytes.NewReader(values["candidate"]))
			if err != nil {
				t.Fatal(err)
			}
			want := expected[pkg.Template.Ref.ID]
			paths := []string{}
			for _, file := range result.Files {
				paths = append(paths, file.Path)
				if !strings.HasPrefix(file.Text, "---\n") || !strings.Contains(file.Text, "Evidence IDs: ") || strings.Contains(file.Text, "b-copy") || strings.Contains(file.Text, "hunter2") {
					t.Fatal("file lacks front matter, cites a copy or leaks a secret", file.Path, file.Text)
				}
			}
			if strings.Join(paths, ",") != strings.Join(want.files, ",") {
				t.Fatal("files", paths)
			}
			held := map[string]string{}
			for _, item := range result.Held {
				if item.ID != "skipped" {
					held[item.ID] = item.HoldReason
				}
			}
			if len(held) != len(want.held) {
				t.Fatal("held", held)
			}
			for id, reason := range want.held {
				if held[id] != reason {
					t.Fatal("hold reason", id, held[id], reason)
				}
			}
			if pkg.Template.Ref.ID == "example-skills" && (runtime.critic != 2 || !strings.HasPrefix(result.Files[0].Text, "---\nname: deploy-service\ndescription: \"Deploying a service\"\n")) {
				t.Fatal("skill critique or front matter", runtime.critic, result.Files[0].Text)
			}
			if pkg.Template.Ref.ID == "example-episodes" {
				// Copies get no record; truncated, unknown and failed recordings are captured.
				reasons := []string{}
				for _, item := range result.Held {
					if item.ID == "skipped" {
						reasons = append(reasons, item.EvidenceIDs[0]+":"+item.HoldReason)
					}
				}
				if strings.Join(reasons, ",") != "d-trunc:truncated_evidence,e-unknown:unknown_provenance,g-broken:record_failed" || runtime.attempts["example-episode-record/record"] != 4 {
					t.Fatal("episode capture", reasons, runtime.attempts)
				}
			}
			if !bytes.Equal(values["candidate"], again()["candidate"]) {
				t.Fatal("recovery changed output")
			}
		})
	}
}

func TestStarterCatalogIsTypedDisjointAndDocumented(t *testing.T) {
	model := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	packages, err := Packages(model)
	if err != nil {
		t.Fatal(err)
	}
	prefixes := map[string]bool{}
	classes := map[string]bool{}
	for i, pkg := range packages {
		c := Classes()[i]
		if pkg.Template.Ref.ID != c.Template || pkg.Template.SuggestedPrefix != c.Prefix || pkg.Template.Validate() != nil || pkg.Template.Evaluation.Mode != "disabled" {
			t.Fatal("template", pkg.Template)
		}
		for other := range prefixes {
			if evolution.PrefixesOverlap(other, c.Prefix) {
				t.Fatal("overlapping starter prefixes", other, c.Prefix)
			}
		}
		prefixes[c.Prefix] = true
		classes[c.MemoryClass] = true
		root := pkg.Workflows[len(pkg.Workflows)-1]
		if err = pkg.Template.SupportsWorkflow(root.Ref, root.Workflow, manifest.SharedAssetsProfile); err != nil {
			t.Fatal(err)
		}
		for _, f := range c.Fields {
			if f.Guidance == "" || f.MaxBytes < 1 {
				t.Fatal("undocumented field", c.Template, f.Name)
			}
		}
		for _, published := range pkg.Workflows {
			for _, n := range published.Workflow.Nodes {
				for _, digest := range []map[string]string{n.InputPorts, n.OutputPorts, n.ItemInputs, n.ItemOutputs} {
					for _, schema := range digest {
						if pkg.Schemas[schema] == nil {
							t.Fatal("unregistered schema", published.Ref.ID, n.ID)
						}
					}
				}
				if n.ImplementationRef != nil && Implementations()[*n.ImplementationRef] == nil {
					t.Fatal("unregistered implementation", n.ID)
				}
			}
		}
	}
	if !classes[Episodic] || !classes[Semantic] || !classes[Procedural] {
		t.Fatal("memory taxonomy incomplete", classes)
	}
}

func TestBuildersRejectForgedEvidenceAndUnsafeNames(t *testing.T) {
	skills, _ := ClassFor("example-skills")
	corpus := Corpus{Episodes: []Episode{{ID: "episode", FamilyID: "family", FamilyKind: "declared", Outcome: "completed", Text: "original"}}}
	items := []Proposal{p("safe", []string{"episode"}, skillAttributes)}
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
		if _, err := verify(skills, corpus, bad); err == nil {
			t.Fatal("unsafe proposal", id)
		}
	}
	bad := []Proposal{p("safe", []string{"episode"}, map[string]string{"when_to_use": "x", "verification": "y", "path": "../../AGENTS.md"})}
	if _, err := verify(skills, corpus, bad); err == nil {
		t.Fatal("undeclared attribute accepted")
	}
	corpus.Episodes[0].Truncated = true
	verified, err := verify(skills, corpus, items)
	if err != nil || verified[0].Status != "held" || verified[0].HoldReason != "truncated_evidence" {
		t.Fatal("truncated evidence promoted", verified, err)
	}
}

func TestEvidenceCopiesCannotInflateSupportAndUnknownFamiliesAreHeld(t *testing.T) {
	facts, _ := ClassFor("example-facts")
	attributes := map[string]string{"kind": "fact", "scope": "global"}
	corpus := Corpus{Episodes: []Episode{{ID: "a", FamilyID: "origin", FamilyKind: "local", Outcome: "completed"}, {ID: "b", FamilyID: "origin", FamilyKind: "copy", Outcome: "completed"}, {ID: "unknown", FamilyID: "unknown", FamilyKind: "unknown", Outcome: "completed"}}}
	result, err := verify(facts, corpus, []Proposal{p("known", []string{"b", "a", "a"}, attributes), p("unknown", []string{"unknown"}, attributes)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result[0].EvidenceIDs) != 1 || result[0].EvidenceIDs[0] != "a" || result[1].Status != "held" || result[1].HoldReason != "unknown_provenance" {
		t.Fatal("family evidence inflated", result)
	}
}

func TestSensitiveContentIsHeldNotPublished(t *testing.T) {
	facts, _ := ClassFor("example-facts")
	corpus := Corpus{Episodes: []Episode{{ID: "a", FamilyID: "a", FamilyKind: "local", Outcome: "completed"}}}
	for _, text := range []string{"-----BEGIN RSA PRIVATE KEY-----", "AKIAABCDEFGHIJKLMNOP", "api_key=abcdef123456", "postgres://admin:pw@db/prod", "contact owner@corp.example", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz"} {
		item := p("claim", []string{"a"}, map[string]string{"kind": "fact", "scope": text[:1]})
		item.Body = "Note: " + text
		result, err := verify(facts, corpus, []Proposal{item})
		if err != nil || result[0].HoldReason != "sensitive_content" {
			t.Fatal("sensitive content promoted", text, result, err)
		}
	}
	clean := p("claim", []string{"a"}, map[string]string{"kind": "fact", "scope": "global"})
	clean.Body = "Rotate the token every 30 days; keys live in the vault."
	if result, err := verify(facts, corpus, []Proposal{clean}); err != nil || result[0].Status != "supported" {
		t.Fatal("ordinary security guidance held", result, err)
	}
}

func TestRecoveryAndAgentHoldsAreRecorded(t *testing.T) {
	corrections, _ := ClassFor("example-corrections")
	corpus := Corpus{Episodes: []Episode{{ID: "a", FamilyID: "a", FamilyKind: "local", Outcome: "completed"}}}
	held := p("maybe", []string{"a"}, correctionAttributes)
	held.Status, held.HoldReason = "held", "Needs Review!"
	result, err := verify(corrections, corpus, []Proposal{held})
	if err != nil || result[0].HoldReason != "held_by_agent" {
		t.Fatal("agent hold reason not normalized", result, err)
	}
	missing := p("partial", []string{"a"}, map[string]string{"scope": "x"})
	if result, err = verify(corrections, corpus, []Proposal{missing}); err != nil || result[0].HoldReason != "missing_field" {
		t.Fatal("incomplete lesson promoted", result, err)
	}
}

func TestEverySampleShowsPromotedFilesAndOneHeldReasonAndEveryNodeIsDocumented(t *testing.T) {
	model := evolution.Revision{ID: "model", Revision: 1, Digest: evolution.Digest([]byte("model"))}
	packages, err := Packages(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		sample, ok := SampleFor(pkg.Template.Ref.ID)
		if !ok || len(sample.Files) == 0 || len(sample.Held) == 0 || sample.Held[0].HoldReason == "" || Summary(pkg.Template.Ref.ID) == "" {
			t.Fatal("sample", pkg.Template.Ref.ID, sample)
		}
		for _, published := range pkg.Workflows {
			docs := NodeDocs(published.Ref.ID)
			for _, n := range published.Workflow.Nodes {
				if docs[n.ID] == "" {
					t.Fatal("undocumented node", published.Ref.ID, n.ID)
				}
			}
		}
	}
}
