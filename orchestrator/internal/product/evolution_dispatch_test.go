package product

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/studioclient"
)

type dispatchTransport struct {
	assignments map[string]evolution.GenerationAssignment
	payloads    map[string][]byte
	calls       int
	loseReply   bool
}

func (d *dispatchTransport) SealGenerationInput(ctx context.Context, ref evolution.ArtifactRef, body []byte) error {
	if evolution.Digest(body) != ref.Digest {
		return ErrConflict
	}
	if d.payloads == nil {
		d.payloads = map[string][]byte{}
	}
	d.payloads[ref.ID] = body
	return nil
}
func (d *dispatchTransport) AdmitGeneration(ctx context.Context, a evolution.GenerationAssignment) (evolution.Run, error) {
	d.calls++
	if d.assignments == nil {
		d.assignments = map[string]evolution.GenerationAssignment{}
	}
	if prior, ok := d.assignments[a.RunID]; ok && !reflect.DeepEqual(prior, a) {
		return evolution.Run{}, ErrConflict
	}
	d.assignments[a.RunID] = a
	if d.loseReply {
		d.loseReply = false
		return evolution.Run{}, errors.New("lost admission response")
	}
	return evolution.Run{ID: a.RunID, GroupID: a.GroupID, BindingID: a.BindingID, BatchID: a.BatchID, Pins: a.Pins, Budget: a.Budget}, nil
}

type dispatchBases struct {
	calls int
	ref   evolution.ContentRef
}

func (d *dispatchBases) Bases(context.Context, studioclient.Projection) (map[string]evolution.ContentRef, error) {
	d.calls++
	return map[string]evolution.ContentRef{d.ref.LibraryID: d.ref}, nil
}
func dispatchAssignments(t *testing.T, s *Store) []evolution.GenerationAssignment {
	t.Helper()
	rows, err := s.db.Query("SELECT assignment FROM generation_dispatches ORDER BY binding_id")
	must(t, err)
	defer rows.Close()
	result := []evolution.GenerationAssignment{}
	for rows.Next() {
		var raw []byte
		must(t, rows.Scan(&raw))
		var a evolution.GenerationAssignment
		must(t, json.Unmarshal(raw, &a))
		result = append(result, a)
	}
	must(t, rows.Err())
	return result
}
func TestEvolutionDispatcherFreezesSharedBatchAndReplaysLostReply(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	activateCorpusCollector(t, s, token, source, 1, true, func(c *evolution.Configuration) {
		sibling := c.Bindings[0]
		sibling.ID = "lessons"
		sibling.Output.Prefix = "lessons"
		c.Bindings = append(c.Bindings, sibling)
		c.Budget.MaxTokens *= 2
		c.Budget.MaxCostMicros *= 2
	})
	native := finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("base"))}}
	remote := &dispatchTransport{loseReply: true}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: remote, Issuer: "https://app"}
	must(t, worker.prepare(ctx))
	frozen := dispatchAssignments(t, s)
	if len(frozen) != 2 || frozen[0].BatchID == "" || frozen[0].BatchID != frozen[1].BatchID || !reflect.DeepEqual(frozen[0].Pins.Inputs, frozen[1].Pins.Inputs) || !reflect.DeepEqual(frozen[0].Pins.InputPorts, frozen[1].Pins.InputPorts) || frozen[0].Pins.Base != frozen[1].Pins.Base || bases.calls != 1 {
		t.Fatal("batch did not share exact inputs/base", frozen, bases.calls)
	}
	if err := worker.dispatch(ctx); err == nil {
		t.Fatal("reply loss absent")
	}
	// Restart the service worker and replay the exact assignment. New source input
	// must remain pending while these generations are admitted or unsettled.
	worker = &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: remote, Issuer: "https://app"}
	for i := 0; i < 4; i++ {
		must(t, worker.dispatch(ctx))
	}
	if len(remote.assignments) != 2 || remote.calls != 3 {
		t.Fatal("admission replay changed identities", remote.calls, remote.assignments)
	}
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	must(t, worker.prepare(ctx))
	if !reflect.DeepEqual(frozen, dispatchAssignments(t, s)) {
		t.Fatal("active generation or frozen assignment replaced")
	}
	// One sibling succeeds; the other fails. Only the successful input is consumed.
	for i, a := range frozen {
		grant, err := s.AuthorizeGeneration(ctx, a)
		must(t, err)
		state := "completed"
		if i == 1 {
			state = "failed"
		}
		must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: "running"}))
		must(t, s.ObserveGeneration(ctx, evolution.GenerationObservation{Grant: grant, Fence: 1, State: state}))
		must(t, s.SettleGeneration(ctx, GenerationSettlement{Grant: grant, Fence: 1, Tokens: 1, CostMicros: 1}))
	}
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	worker.prepareCursor = ""
	must(t, worker.prepare(ctx))
	if len(dispatchAssignments(t, s)) != 4 {
		t.Fatal("post-settlement delta not prepared")
	}
	first, err := s.CorpusFrontier(ctx, token, "fixture-users", frozen[0].BindingID, source.ID)
	must(t, err)
	other, err := s.CorpusFrontier(ctx, token, "fixture-users", frozen[1].BindingID, source.ID)
	must(t, err)
	if first.Through == 0 || other.Through != 0 {
		t.Fatal("sibling consumption coupled", first, other)
	}
}
func TestCorpusBoundedSnapshotRetainsRevisionBeforeLateEvidence(t *testing.T) {
	s, _, incarnation, command, token, source := corpusFixture(t)
	native := finishCorpusExecution(t, s, incarnation, command)
	first, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "first", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	pending, err := s.PendingCaptures(ctx, 100)
	must(t, err)
	must(t, (&Captures{Store: s, Backend: captureReader{bytes: native}, Directory: t.TempDir()}).Export(ctx, pending[0]))
	late, err := s.CaptureCorpusEpisode(ctx, token, "fixture-users", CorpusCaptureInput{RequestID: "late", SourceID: source.ID, SourceRevision: 1, ExecutionID: command.CommandID})
	must(t, err)
	if late.Revision <= first.Revision {
		t.Fatal("late evidence missing")
	}
	snapshot, err := s.SealCorpusSnapshot(ctx, token, "fixture-users", CorpusSnapshotInput{RequestID: "bounded", SourceID: source.ID, SourceRevision: 1, ThroughSequence: first.Sequence, Limit: 10})
	must(t, err)
	if len(snapshot.Members) != 1 || snapshot.Members[0].Revision != first.Revision || snapshot.Through != first.Sequence {
		t.Fatal("bounded snapshot selected future evidence", snapshot)
	}
}
func TestEvolutionDispatcherPreparationAuditRollsBackAndRevocationStopsExport(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	activateCorpusCollector(t, s, token, source, 1, true)
	finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	remote := &dispatchTransport{}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: remote, Issuer: "https://app"}
	_, err := s.db.Exec(`CREATE TRIGGER fail_dispatch_audit BEFORE INSERT ON audit_events WHEN NEW.action='generation.dispatch.prepared' BEGIN SELECT RAISE(ABORT,'fault'); END;`)
	must(t, err)
	if err = worker.prepare(ctx); err == nil {
		t.Fatal("audit failure absent")
	}
	if len(dispatchAssignments(t, s)) != 0 || triggerItems(t, s, token)[0].State != "pending" {
		t.Fatal("audit failure froze assignment")
	}
	_, err = s.db.Exec("DROP TRIGGER fail_dispatch_audit")
	must(t, err)
	worker.prepareCursor = ""
	must(t, worker.prepare(ctx))
	_, err = s.SetEvolutionGroupGrant(ctx, token, "fixture-users", EvolutionGroupGrantInput{RequestID: "deny", AccountID: scope.Actor, Action: "source.read", Effect: "deny", ExpectedRevision: 1})
	must(t, err)
	if err = worker.dispatch(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked evidence export", err)
	}
	if len(remote.payloads) != 0 || remote.calls != 0 {
		t.Fatal("revoked evidence transmitted")
	}
	if item := triggerItems(t, s, token)[0]; item.DispatchError != "authority_denied" {
		t.Fatal("missing dispatch failure metadata", item)
	}
}

func TestEvolutionDisableSupersedesUnadmittedButDrainsAdmittedDispatch(t *testing.T) {
	s, scope, incarnation, command, token, source := corpusFixture(t)
	must(t, grantFixture(s, ctx, token, "account", scope.Actor, "library", "volume.read", "allow", 0))
	activateCorpusCollector(t, s, token, source, 1, true, func(c *evolution.Configuration) {
		sibling := c.Bindings[0]
		sibling.ID = "lessons"
		sibling.Output.Prefix = "lessons"
		c.Bindings = append(c.Bindings, sibling)
		c.Budget.MaxTokens *= 2
		c.Budget.MaxCostMicros *= 2
	})
	finishCorpusExecution(t, s, incarnation, command)
	must(t, (&CorpusCollector{Store: s}).Step(ctx))
	must(t, (&EvolutionTriggerWorker{Store: s}).Step(ctx))
	bases := &dispatchBases{ref: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest(nil)}}
	remote := &dispatchTransport{loseReply: true}
	worker := &EvolutionDispatcher{Store: s, Studio: bases, Evaluation: remote, Issuer: "https://app"}
	must(t, worker.prepare(ctx))
	if err := worker.dispatch(ctx); err == nil {
		t.Fatal("missing lost reply")
	}
	_, err := s.DisableEvolution(ctx, token, "fixture-users", DisableEvolutionInput{RequestID: "disable", ExpectedRevision: 1})
	must(t, err)
	var pending, superseded int
	must(t, s.db.QueryRow("SELECT count(*) FROM generation_dispatches WHERE state='pending'").Scan(&pending))
	must(t, s.db.QueryRow("SELECT count(*) FROM generation_dispatches WHERE state='superseded'").Scan(&superseded))
	if pending != 1 || superseded != 1 {
		t.Fatal("disable lost admitted replay or retained unadmitted work", pending, superseded)
	}
	worker.dispatchCursor = ""
	must(t, worker.dispatch(ctx))
	if remote.calls != 2 || len(remote.assignments) != 1 {
		t.Fatal("disable admitted new sibling", remote.calls, remote.assignments)
	}
}
