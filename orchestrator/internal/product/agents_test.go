package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"harness-platform/orchestrator/internal/agentwire"
)

// agentServiceFixture is agentFixture with a Project profile that declares a
// service audience, so grant issuance has a declared destination to resolve.
func agentServiceFixture(t *testing.T) (*Store, Scope, Incarnation, agentwire.Frame) {
	t.Helper()
	return agentFixtureWith(t, serviceProfile())
}
func agentFixture(t *testing.T) (*Store, Scope, Incarnation, agentwire.Frame) {
	t.Helper()
	return agentFixtureWith(t, testProfile())
}
func agentFixtureWith(t *testing.T, profile Profile) (*Store, Scope, Incarnation, agentwire.Frame) {
	t.Helper()
	l, scope, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, scope, profile))
	drive(t, l, scope.ProjectID)
	_, err := l.Store.CreateSession(ctx, scope, "agent", "Agent")
	must(t, err)
	_, err = l.Store.AdmitTurn(ctx, scope, "agent", "request", "hello", false)
	must(t, err)
	i, err := l.Store.reserveAgent(ctx, scope, "agent")
	must(t, err)
	_, err = l.Store.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, i.ID)
	must(t, err)
	i.State = "running"
	command, err := l.Store.agentCommand(ctx, i)
	must(t, err)
	return l.Store, scope, i, command
}
func record(i Incarnation, c agentwire.Frame, seq uint64, kind, payload string) agentwire.Frame {
	return agentwire.Frame{Kind: "record", Session: i.SessionID, Incarnation: i.ID, Owner: i.Owner, Sequence: seq, CommandID: c.CommandID, TurnID: c.TurnID, State: kind, Payload: json.RawMessage(payload)}
}
func TestCommittedIngestDedupAndUnknownRecords(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	applied := record(i, c, 1, "command.applied", `{"success":true}`)
	seq, err := s.IngestAgent(ctx, i, applied)
	must(t, err)
	if seq != 1 {
		t.Fatal(seq)
	}
	seq, err = s.IngestAgent(ctx, i, applied)
	must(t, err)
	if seq != 1 {
		t.Fatal(seq)
	}
	altered := applied
	altered.Payload = json.RawMessage(`{"different":true}`)
	_, err = s.IngestAgent(ctx, i, altered)
	wantError(t, err, ErrConflict)
	gap := applied
	gap.Sequence = 3
	_, err = s.IngestAgent(ctx, i, gap)
	wantError(t, err, ErrConflict)
	stale := applied
	stale.Owner++
	_, err = s.IngestAgent(ctx, i, stale)
	wantError(t, err, ErrInvalid)
	foreign := record(i, c, 2, "native", `{}`)
	foreign.TurnID = "foreign"
	_, err = s.IngestAgent(ctx, i, foreign)
	wantError(t, err, ErrNotFound)
	assistant := record(i, c, 2, "native", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"trace"},{"type":"text","text":"reply"}]}}`)
	_, err = s.IngestAgent(ctx, i, assistant)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, assistant)
	must(t, err)
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"sha256:%064d","size":80,"outcome":"completed"}`, c.CommandID, 0)
	completed := record(i, c, 3, "command.completed", payload)
	_, err = s.IngestAgent(ctx, i, completed)
	must(t, err)
	_, err = s.IngestAgent(ctx, i, completed)
	must(t, err)
	messages, err := s.Messages(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Content != "reply" || !json.Valid(messages[1].Native) {
		t.Fatal(messages)
	}
	turns, err := s.Turns(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if turns[0].State != "completed" {
		t.Fatal(turns)
	}
	// Host crash after commit and before ACK cannot duplicate messages or capture.
	current, err := liveIncarnation(ctx, s.reader, i.SessionID)
	must(t, err)
	if current.Ingested != 3 {
		t.Fatal(current)
	}
	var captures int
	must(t, s.db.QueryRow(`SELECT count(*) FROM native_captures`).Scan(&captures))
	if captures != 1 {
		t.Fatal(captures)
	}
}
func TestNativeReceiptDoesNotMaterializeAndUnknownScopesSession(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	messages, err := s.Messages(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if len(messages) != 0 {
		t.Fatal("transport send materialized user", messages)
	}
	_, err = s.CreateSession(ctx, scope, "sibling", "Agent")
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, c, 1, "command.unknown", `{"error":"partial native write"}`))
	must(t, err)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	if child.State != "blocked" {
		t.Fatal(child)
	}
	sibling, err := s.Session(ctx, scope, "sibling")
	must(t, err)
	if sibling.State != "idle" {
		t.Fatal(sibling)
	}
	p, err := s.Project(ctx, scope)
	must(t, err)
	if p.State != "active" {
		t.Fatal(p)
	}
}
func TestSteerReceiptReleaseRaceAndAppliedProjection(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	source, err := s.AdmitTurn(ctx, scope, i.SessionID, "steer-source", "steer this", true)
	must(t, err)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.Steer(ctx, scope, i.SessionID, source.ID, "steer-request", child.Revision))
	child, err = s.Session(ctx, scope, i.SessionID)
	must(t, err)
	wantError(t, s.ReleaseTurn(ctx, scope, i.SessionID, source.ID, child.Revision), ErrConflict)
	steer, err := s.agentCommand(ctx, i)
	must(t, err)
	var state string
	must(t, s.db.QueryRow(`SELECT state FROM turns WHERE id=?`, source.ID).Scan(&state))
	if state != "held" {
		t.Fatal("send retired steering source")
	}
	_, err = s.IngestAgent(ctx, i, record(i, steer, 2, "command.applied", `{"success":true}`))
	must(t, err)
	must(t, s.db.QueryRow(`SELECT state FROM turns WHERE id=?`, source.ID).Scan(&state))
	if state != "completed" {
		t.Fatal(state)
	}
	// Replaying the request after the Session revision advanced remains idempotent.
	must(t, s.Steer(ctx, scope, i.SessionID, source.ID, "steer-request", 1))
	messages, err := s.Messages(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if len(messages) != 2 || messages[1].Content != "steer this" {
		t.Fatal(messages)
	}
}
func TestAbortHoldsQueueAndWaitsForNativeCancellation(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "queued", "next", false)
	must(t, err)
	child, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	must(t, s.Interrupt(ctx, scope, i.SessionID, "stop", child.Revision))
	turns, err := s.Turns(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if turns[0].State != "stopping" || turns[1].State != "held" {
		t.Fatal(turns)
	}
	abort, err := s.agentCommand(ctx, i)
	must(t, err)
	if abort.CommandID == c.CommandID {
		t.Fatal(abort)
	}
	payload := fmt.Sprintf(`{"path":"/runtime/sessions/agent/native/captures/%s.jsonl","digest":"sha256:%064d","size":80,"outcome":"canceled"}`, c.CommandID, 0)
	_, err = s.IngestAgent(ctx, i, record(i, c, 2, "command.completed", payload))
	must(t, err)
	_, err = s.IngestAgent(ctx, i, record(i, abort, 3, "command.applied", `{"success":true}`))
	must(t, err)
	turns, err = s.Turns(ctx, scope, i.SessionID, 0, 100)
	must(t, err)
	if turns[0].State != "canceled" || turns[1].State != "held" {
		t.Fatal(turns)
	}
	if _, err = s.agentCommand(ctx, i); !errors.Is(err, ErrNotFound) {
		t.Fatal("held prompt auto-dispatched", err)
	}
}
