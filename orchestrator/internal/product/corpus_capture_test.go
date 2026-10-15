package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/agentwire"
)

func corpusFixture(t *testing.T) (*Store, Scope, Incarnation, agentwire.Frame, string, evolution.SourceBinding) {
	t.Helper()
	s, scope, incarnation, command := agentFixture(t)
	_, err := s.db.Exec("UPDATE accounts SET role='admin' WHERE id=?", scope.Actor)
	must(t, err)
	account, err := s.Account(ctx, scope.Actor)
	must(t, err)
	_, token, err := s.Authenticate(ctx, account.Username, "fixture-password")
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "source-read", AccountID: account.ID, Action: "source.read", Effect: "allow"})
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: scope.ProjectID, PolicyRevision: 1, AudienceGroups: []string{"fixture-users"}, DerivativePublication: true}
	_, err = s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "source", Source: source})
	must(t, err)
	_, err = s.IngestAgent(ctx, incarnation, record(incarnation, command, 1, "command.applied", `{"success":true}`))
	must(t, err)
	return s, scope, incarnation, command, token, source
}
func TestCorpusCaptureSealsRevisionsAndAuditsOnlyApprovedSource(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	native := `{"type":"session","id":"session"}` + "\n" + `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden reasoning sentinel"},{"type":"text","text":"Task completed"},{"type":"toolCall","name":"read","arguments":{"path":"file"}}]}}` + "\n" + `{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"observable tool result"}]}}` + "\n"
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":%d}`, command.CommandID, evolution.Digest([]byte(native)), len(native))
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 2, "command.completed", payload))
	must(t, err)
	input := CorpusCaptureInput{RequestID: "capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID}
	first, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", input)
	must(t, err)
	if first.CaptureState != "missing" || !first.Episode.Truncated || first.Episode.FamilyID != command.TurnID || first.Revision != 1 {
		t.Fatal(first)
	}
	replay, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", input)
	must(t, err)
	if replay.Digest != first.Digest || replay.Sequence != first.Sequence {
		t.Fatal("capture replay changed")
	}
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	input.RequestID = "late-capture"
	late, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", input)
	must(t, err)
	if late.Revision != 2 || late.Sequence <= first.Sequence || late.Episode.Truncated || late.CaptureState != "complete" || !strings.Contains(late.Episode.Text, "observable tool result") || strings.Contains(late.Episode.Text, "hidden reasoning sentinel") {
		t.Fatal(late)
	}
	snapshot, err := s.SealCorpusSnapshot(ctx, token, "fixture-users", CorpusSnapshotInput{RequestID: "seal", SourceID: source.ID, SourceRevision: 1, AfterSequence: 0, Limit: 50})
	must(t, err)
	if len(snapshot.Members) != 1 || snapshot.Members[0].Revision != 2 || snapshot.Through != late.Sequence {
		t.Fatal(snapshot)
	}
	must(t, s.AuthorizeGenerationInput(ctx, snapshot.Artifact))
	changedArtifact := snapshot.Artifact
	changedArtifact.Digest = evolution.Digest([]byte("other"))
	if err = s.AuthorizeGenerationInput(ctx, changedArtifact); !errors.Is(err, ErrForbidden) {
		t.Fatal("substituted snapshot transfer", err)
	}
	raw, err := s.CorpusSnapshotContent(ctx, token, "fixture-users", snapshot.ID)
	must(t, err)
	var corpus evolution.Corpus
	must(t, json.Unmarshal(raw, &corpus))
	if len(corpus.Episodes) != 1 || corpus.Episodes[0] != late.Episode || evolution.Digest(raw) != snapshot.Artifact.Digest {
		t.Fatal(corpus)
	}
	if _, err = s.CorpusSnapshotContent(ctx, token, "other", snapshot.ID); err == nil {
		t.Fatal("cross-group snapshot read")
	}
	source.PolicyRevision = 2
	source.DerivativePublication = false
	_, err = s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "revoke", ExpectedRevision: 1, Source: source})
	must(t, err)
	if err = s.AuthorizeGenerationInput(ctx, snapshot.Artifact); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked transfer authorized", err)
	}
	if data, err := s.CorpusSnapshotContent(ctx, token, "fixture-users", snapshot.ID); !errors.Is(err, ErrForbidden) || data != nil {
		t.Fatal("revoked snapshot read", err)
	}
	var audited int
	must(t, s.db.QueryRow("SELECT count(*) FROM audit_events WHERE action IN ('corpus.capture','corpus.seal','corpus.export')").Scan(&audited))
	if audited != 4 {
		t.Fatal("capture/export audit mismatch", audited)
	}
}
func TestCorpusSnapshotFailureDoesNotAdvanceAndCorruptionCannotEscape(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	native := `{"type":"message","message":{"role":"user","content":"task"}}` + "\n"
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":%d}`, command.CommandID, evolution.Digest([]byte(native)), len(native))
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 2, "command.completed", payload))
	must(t, err)
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	var path string
	must(t, s.db.QueryRow("SELECT host_path FROM native_captures").Scan(&path))
	must(t, os.WriteFile(path, []byte("corrupt"), 0600))
	if value, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "bad", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID}); err == nil || value.Episode.Text != "" {
		t.Fatal("corrupt capture escaped", value, err)
	}
	var count int
	must(t, s.db.QueryRow("SELECT count(*) FROM corpus_episodes").Scan(&count))
	if count != 0 {
		t.Fatal("corrupt evidence persisted")
	}
	must(t, os.WriteFile(path, []byte(native), 0600))
	_, err = s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "good", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER fail_snapshot BEFORE INSERT ON corpus_snapshots BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if _, err = s.SealCorpusSnapshot(ctx, token, "fixture-users", CorpusSnapshotInput{RequestID: "seal", SourceID: source.ID, SourceRevision: 1, Limit: 10}); err == nil {
		t.Fatal("snapshot fault absent")
	}
	must(t, s.db.QueryRow("SELECT count(*) FROM generation_input_grants").Scan(&count))
	if count != 0 {
		t.Fatal("unsealed snapshot got a use grant")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_snapshot")
	must(t, err)
	sealed, err := s.SealCorpusSnapshot(ctx, token, "fixture-users", CorpusSnapshotInput{RequestID: "seal", SourceID: source.ID, SourceRevision: 1, Limit: 10})
	must(t, err)
	replay, err := s.SealCorpusSnapshot(ctx, token, "fixture-users", CorpusSnapshotInput{RequestID: "seal", SourceID: source.ID, SourceRevision: 1, Limit: 10})
	must(t, err)
	if replay.ID != sealed.ID {
		t.Fatal("snapshot replay changed")
	}
	for _, binding := range []string{"skills", "corrections"} {
		frontier, err := s.CorpusFrontier(context.Background(), token, "fixture-users", binding, source.ID)
		must(t, err)
		if frontier.Through != 0 {
			t.Fatal("seal consumed binding input", frontier)
		}
	}
}

func TestCorpusConsumptionAdvancesOnlySuccessfulPinnedBinding(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 2, "command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":0}`, command.CommandID, evolution.Digest(nil))))
	must(t, err)
	_, err = s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	snapshot, err := s.SealCorpusSnapshot(ctx, token, "fixture-users", CorpusSnapshotInput{RequestID: "seal", SourceID: source.ID, SourceRevision: 1, Limit: 10})
	must(t, err)
	budget := evolution.Budget{MaxTokens: 10, MaxCostMicros: 10, MaxAttempts: 1, TimeoutSeconds: 60, MaxArtifactBytes: 1 << 20}
	groupBudget := budget
	groupBudget.MaxTokens = 20
	groupBudget.MaxCostMicros = 20
	ref := evolution.Revision{ID: "ref", Revision: 1, Digest: evolution.Digest([]byte("ref"))}
	config := evolution.Configuration{GroupID: "fixture-users", Revision: 1, Sources: []evolution.SourceBinding{source}, Budget: groupBudget}
	for _, id := range []string{"skills", "lessons"} {
		config.Bindings = append(config.Bindings, evolution.Binding{ID: id, Enabled: true, Template: ref, Workflow: ref, Service: ref, Parameters: json.RawMessage(`{}`), SourceIDs: []string{source.ID}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: id, Profile: "authored-v1", ReservationGeneration: 1}, Evaluation: evolution.EvaluationPolicy{Mode: "disabled", ReferenceStrategy: "none"}, Budget: budget, Trigger: "manual"})
	}
	pending, err := s.ActivateEvolutionConfig(ctx, token, "fixture-users", EvolutionActivationInput{RequestID: "activate", Configuration: config})
	must(t, err)
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, participant, pending.DesiredDigest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	for _, binding := range config.Bindings {
		pins := evolution.RunPins{ConfigurationRevision: 1, ConfigurationDigest: pending.DesiredDigest, SourcePolicyRevision: 1, Template: ref, Workflow: ref, Service: ref, Inputs: []evolution.ArtifactRef{snapshot.Artifact}, InputPorts: map[string]evolution.ArtifactRef{"corpus": snapshot.Artifact}, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("base"))}, Output: binding.Output, Evaluation: binding.Evaluation}
		assignment := evolution.GenerationAssignment{RunID: binding.ID, GroupID: "fixture-users", BindingID: binding.ID, OccurrenceID: "manual", Requester: evolution.Principal{Issuer: "https://app", Subject: scope.Actor, Kind: "human"}, Pins: pins, Budget: budget}
		grant, err := s.AuthorizeGeneration(ctx, assignment)
		must(t, err)
		must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
		if err = s.ConsumeCorpusSnapshot(ctx, grant, snapshot.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("running work consumed source", err)
		}
		state := "completed"
		if binding.ID == "lessons" {
			state = "failed"
		}
		must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: state}))
		if state == "completed" {
			must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 1, Tokens: 1, CostMicros: 1}))
		}
		err = s.ConsumeCorpusSnapshot(ctx, grant, snapshot.ID)
		if state == "failed" {
			if !errors.Is(err, ErrConflict) {
				t.Fatal("failed sibling consumed source", err)
			}
		} else {
			must(t, err)
			must(t, s.ConsumeCorpusSnapshot(ctx, grant, snapshot.ID))
		}
	}
	skills, err := s.CorpusFrontier(ctx, token, "fixture-users", "skills", source.ID)
	must(t, err)
	lessons, err := s.CorpusFrontier(ctx, token, "fixture-users", "lessons", source.ID)
	must(t, err)
	if skills.Through != snapshot.Through || skills.SnapshotID != snapshot.ID || lessons.Through != 0 {
		t.Fatal("sibling frontier coupled", skills, lessons)
	}
}

func TestCorpusAuditFailureReturnsNoSensitiveContent(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 2, "command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":0}`, command.CommandID, evolution.Digest(nil))))
	must(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER fail_capture_audit BEFORE INSERT ON audit_events WHEN NEW.action='corpus.capture' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	result, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	if err == nil || result.Episode.Text != "" {
		t.Fatal("audit failure leaked capture", result, err)
	}
	var count int
	must(t, s.db.QueryRow("SELECT count(*) FROM corpus_episodes").Scan(&count))
	if count != 0 {
		t.Fatal("audit failure retained capture")
	}
}

func TestCorpusConfigurationManagerNeedsExplicitEvidenceGrant(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 2, "command.completed", fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"%s","size":0}`, command.CommandID, evolution.Digest(nil))))
	must(t, err)
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "deny-source-read", AccountID: scope.Actor, Action: "source.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	result, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "capture", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	if !errors.Is(err, ErrForbidden) || result.Episode.Text != "" {
		t.Fatal("administrator/config manager implicitly read evidence", result, err)
	}
}
