package product

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCorpusNativeCaptureExcludesPriorAndImportedHistory(t *testing.T) {
	prior := `{"type":"message","message":{"role":"assistant","content":"prior private sentinel"}}` + "\n"
	selected := `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden sentinel"},{"type":"text","text":"selected observation"}]}}` + "\n"
	prior = `{"type":"custom","customType":"external","data":["unrelated metadata"]}` + "\n" + prior
	raw := scopedNativeFixture("old", prior) + scopedNativeFixture("selected", selected) + scopedNativeFixture("future", prior)
	text, partial, err := observableNativeCapture([]byte(raw), "selected")
	must(t, err)
	if partial || text != "assistant: selected observation\n" || strings.Contains(text, "sentinel") {
		t.Fatal("capture mixed executions", text, partial)
	}
	text, partial, err = observableNativeCapture([]byte(prior), "selected")
	must(t, err)
	if !partial || text != "" {
		t.Fatal("unscoped snapshot exported", text, partial)
	}
	raw = scopedNativeFixture("selected", selected)
	// Strip the final marker without depending on the JSON object's key order.
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	raw = strings.Join(lines[:len(lines)-1], "\n")
	text, partial, err = observableNativeCapture([]byte(raw), "selected")
	must(t, err)
	if !partial || !strings.Contains(text, "selected observation") {
		t.Fatal("missing end not labelled", text, partial)
	}
	if _, _, err = observableNativeCapture([]byte(scopedNativeFixture("selected", scopedNativeFixture("other", selected))), "selected"); !errors.Is(err, ErrConflict) {
		t.Fatal("overlapping native execution accepted", err)
	}
}
func TestCorpusExclusionsRejectSelectedExecutionWithoutStallingCollector(t *testing.T) {
	for _, kind := range []string{"sessions", "turns", "executions"} {
		t.Run(kind, func(t *testing.T) {
			s, _, incarnation, command, token, source := corpusFixture(t)
			id := command.CommandID
			if kind == "sessions" {
				id = incarnation.SessionID
			}
			if kind == "turns" {
				id = command.TurnID
			}
			source.PolicyRevision = 2
			source.Exclusions = []string{kind + "/" + id}
			_, err := s.SetEvolutionSourcePolicy(ctx, token, "fixture-users", EvolutionSourcePolicyInput{RequestID: "exclude", ExpectedRevision: 1, Source: source})
			must(t, err)
			activateCorpusCollector(t, s, token, source, 1, true)
			finishCorpusExecution(t, s, incarnation, command)
			if result, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "capture-excluded", SourceID: source.ID, SourceRevision: 2, ExecutionID: command.CommandID}); !errors.Is(err, ErrForbidden) || result.Episode.Text != "" {
				t.Fatal("excluded evidence exported", result, err)
			}
			before := collectorStatus(t, s, token)
			must(t, (&CorpusCollector{Store: s}).Step(ctx))
			after := collectorStatus(t, s, token)
			if after.AfterEvent <= before.AfterEvent || countCorpus(t, s, "corpus_episodes") != 0 || countCorpus(t, s, "corpus_snapshots") != 0 {
				t.Fatal("excluded event blocked cursor or became evidence", after)
			}
		})
	}
}
func TestConversationCopiesRetainEvidenceFamilyAcrossMultipleImports(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	service := &Shares{Store: s, Directory: t.TempDir()}
	_, token, err := service.Create(ctx, scope, "shared", nil, time.Now().Add(time.Hour))
	must(t, err)
	first, err := s.CreateConversationCopy(ctx, "bob", CopyRequest{ID: "copy-one", Kind: "publicSnapshot", Token: token, ProjectID: "copy-one-project", ProjectTitle: "First", Title: "First copy"})
	must(t, err)
	must(t, service.CopyStep(ctx))
	var firstTurn string
	must(t, s.db.QueryRow("SELECT id FROM turns WHERE session_id=? ORDER BY sequence LIMIT 1", first.SessionID).Scan(&firstTurn))
	var original string
	must(t, s.db.QueryRow("SELECT id FROM turns WHERE session_id='shared' ORDER BY sequence LIMIT 1").Scan(&original))
	family, kind, err := corpusEvidenceFamily(ctx, s.reader, firstTurn)
	must(t, err)
	if family != original || kind != "copy" {
		t.Fatal("first copy changed evidence identity", family, kind)
	}
	_, secondToken, err := service.Create(ctx, Scope{Actor: "bob", ProjectID: first.ProjectID}, first.SessionID, nil, time.Now().Add(time.Hour))
	must(t, err)
	second, err := s.CreateConversationCopy(ctx, "bob", CopyRequest{ID: "copy-two", Kind: "publicSnapshot", Token: secondToken, ProjectID: "copy-two-project", ProjectTitle: "Second", Title: "Second copy"})
	must(t, err)
	must(t, service.CopyStep(ctx))
	var secondTurn string
	must(t, s.db.QueryRow("SELECT id FROM turns WHERE session_id=? ORDER BY sequence LIMIT 1", second.SessionID).Scan(&secondTurn))
	family, kind, err = corpusEvidenceFamily(ctx, s.reader, secondTurn)
	must(t, err)
	if family != original || kind != "copy" || firstTurn == secondTurn {
		t.Fatal("second copy became independent evidence", family, kind)
	}
	if _, err = s.db.Exec("UPDATE turn_evidence_families SET family_id='forged' WHERE turn_id=?", secondTurn); err == nil {
		t.Fatal("copy family mutable")
	}
	native, err := s.ImportedNativeHistory(ctx, second.SessionID, "/workspace")
	must(t, err)
	newText := `{"type":"message","message":{"role":"assistant","content":"new execution"}}` + "\n"
	native = append(native, []byte(scopedNativeFixture("new", newText))...)
	text, partial, err := observableNativeCapture(native, "new")
	must(t, err)
	if partial || text != "assistant: new execution\n" {
		t.Fatal("copied history leaked into new evidence", text, partial)
	}
}

func TestCorpusStoredMessagesDoNotMixRetryExecutions(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	finishCorpusExecution(t, s, incarnation, command)
	_, err := s.IngestAgent(ctx, incarnation, record(incarnation, command, 3, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"selected-only observation"}]}}`))
	must(t, err)
	_, err = s.db.Exec("INSERT INTO agent_records VALUES(?,4,'fixture','other-attempt',?,'native','{}')", incarnation.ID, command.TurnID)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) SELECT ?,project_id,session_id,id,'assistant','other attempt sentinel',999,created_at FROM turns WHERE id=?`, incarnation.ID+"/4", command.TurnID)
	must(t, err)
	captured, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "isolated", SourceID: source.ID, SourceRevision: source.PolicyRevision, ExecutionID: command.CommandID})
	must(t, err)
	if !strings.Contains(captured.Episode.Text, "selected-only observation") || strings.Contains(captured.Episode.Text, "other attempt sentinel") {
		t.Fatal("stored retry evidence leaked", captured)
	}
}
