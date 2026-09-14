package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// stoppedSessionFixture builds the state the ↑ button acts on: one turn executing
// and `queuedContents` behind it, with the whole queue parked by a real Stop.
//
// It goes through the HTTP handlers rather than the store so the wiring under test
// is the wiring the browser hits — route, authorization, bridge delivery and all.
func stoppedSessionFixture(t *testing.T, ctx context.Context, srv *Server, st *store.Store, sessionID string, contents ...string) []int64 {
	t.Helper()
	var turnIDs []int64
	for _, content := range contents {
		body := strings.NewReader(`{"content":` + strconv.Quote(content) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/messages", body)
		rec := httptest.NewRecorder()
		srv.sendMessage(rec, req, sessionID)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("send %q: expected 202, got %d body %s", content, rec.Code, rec.Body.String())
		}
		var response struct {
			TurnID int64 `json:"turn_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode send response: %v", err)
		}
		turnIDs = append(turnIDs, response.TurnID)
	}
	waitForSessionStatus(t, ctx, st, sessionID, string(sessionstate.RunningActive))
	// No bridge runs in this fixture, so promote the head turn by hand to get the
	// shape Stop faces: one turn executing, the rest queued behind it.
	if _, err := st.DBForTest().ExecContext(ctx,
		`UPDATE turns SET status = 'running', ack_started_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), turnIDs[0]); err != nil {
		t.Fatalf("mark head turn running: %v", err)
	}
	return turnIDs
}

// pressStop drives the interrupt endpoint the way the Stop button does.
func pressStop(t *testing.T, srv *Server, sessionID string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/interrupt", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, labUserID))
	rec := httptest.NewRecorder()
	srv.interruptSession(rec, req, sessionID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("interrupt: expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
}

// pressSend drives the ↑ button on one queue row and returns the recorder so the
// caller can assert on status and body.
func pressSend(t *testing.T, srv *Server, sessionID string, turnID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+sessionID+"/turns/"+strconv.FormatInt(turnID, 10)+"/send", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, labUserID))
	rec := httptest.NewRecorder()
	srv.sendQueuedTurn(rec, req, sessionID, turnID)
	return rec
}

// bridgeRoot is where the host writes turn-control commands for the session's
// active generation — the file the sandbox watcher reads.
func bridgeRoot(t *testing.T, ctx context.Context, st *store.Store, sessionID string) string {
	t.Helper()
	session, err := st.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	details, err := st.GetRuntimeGenerationDetails(ctx, sessionID, session.ActiveGenerationID)
	if err != nil {
		t.Fatalf("get generation details: %v", err)
	}
	return details.BridgeDirPath
}

// Stop's whole point after this change: the abort travels to the sandbox as a
// turn-control command and the generation survives. The old handler destroyed the
// generation instead, which charged every Stop a cold start.
func TestInterruptSessionDeliversAbortAndKeepsTheSandbox(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)
	turnIDs := stoppedSessionFixture(t, ctx, srv, st, session.ID, "running work")
	root := bridgeRoot(t, ctx, st, session.ID)

	pressStop(t, srv, session.ID)

	control, err := bridge.ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read turn control: %v", err)
	}
	if len(control.Commands) != 1 {
		t.Fatalf("turn control log = %+v, want one abort", control.Commands)
	}
	// Named turn, not "abort whatever is running": a slow delivery must not end the
	// turn that came after the one the user stopped.
	if got := control.Commands[0]; got.Command != bridge.TurnControlAbort || got.TurnID != turnIDs[0] {
		t.Fatalf("control command = %+v, want abort for turn %d", got, turnIDs[0])
	}
	// The generation is untouched, so the next prompt reuses a warm sandbox.
	after, err := st.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if after.ActiveGenerationID == "" {
		t.Fatalf("Stop retired the generation, so the next prompt would cold-start")
	}
	status, err := st.GetRuntimeGenerationStatus(ctx, session.ID, after.ActiveGenerationID)
	if err != nil {
		t.Fatalf("get generation status: %v", err)
	}
	if status != "idle" {
		t.Fatalf("generation status after Stop = %q, want idle", status)
	}
}

// With nothing executing, ↑ releases the parked row: the worker will claim it on
// its own, so the endpoint hands it over and delivers nothing to the sandbox.
func TestSendQueuedTurnReleasesWhenNothingIsRunning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)
	turnIDs := stoppedSessionFixture(t, ctx, srv, st, session.ID, "running work", "queued work")
	root := bridgeRoot(t, ctx, st, session.ID)
	pressStop(t, srv, session.ID)

	rec := pressSend(t, srv, session.ID, turnIDs[1])
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send queued turn: expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status  string                       `json:"status"`
		Runtime store.SessionRuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if response.Status != "released" {
		t.Fatalf("status = %q, want released", response.Status)
	}
	if len(response.Runtime.QueuedTurns) != 1 || response.Runtime.QueuedTurns[0].Parked {
		t.Fatalf("released row is still parked: %+v", response.Runtime.QueuedTurns)
	}
	// A release is not a steer, so nothing new reaches the sandbox: the abort from
	// the Stop above is the only command in the log.
	control, err := bridge.ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read turn control: %v", err)
	}
	if len(control.Commands) != 1 || control.Commands[0].Command != bridge.TurnControlAbort {
		t.Fatalf("release wrote a control command: %+v", control.Commands)
	}
}

// With a turn executing, the same ↑ steers instead: the text reaches the running
// turn, so the work already done survives, and the queue row retires because it
// has been delivered.
func TestSendQueuedTurnSteersIntoTheRunningTurn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)
	turnIDs := stoppedSessionFixture(t, ctx, srv, st, session.ID, "running work", "actually, do this instead")
	root := bridgeRoot(t, ctx, st, session.ID)

	rec := pressSend(t, srv, session.ID, turnIDs[1])
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send queued turn: expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status  string                       `json:"status"`
		Message store.Message                `json:"message"`
		Runtime store.SessionRuntimeSnapshot `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if response.Status != "steered" {
		t.Fatalf("status = %q, want steered", response.Status)
	}
	// The text is in the transcript now: the agent has been told, so the user has to
	// see what was said. A steered prompt is the one queued row that materializes
	// without ever being dispatched as a turn.
	if response.Message.Content != "actually, do this instead" || response.Message.Role != "user" {
		t.Fatalf("steered message = %+v", response.Message)
	}
	// The turn it was steered into is still running: that is the whole difference
	// from stop-and-resend.
	if response.Runtime.ActiveTurn == nil || response.Runtime.ActiveTurn.ID != turnIDs[0] {
		t.Fatalf("steer disturbed the running turn: %+v", response.Runtime.ActiveTurn)
	}
	if len(response.Runtime.QueuedTurns) != 0 {
		t.Fatalf("steered row is still queued: %+v", response.Runtime.QueuedTurns)
	}
	control, err := bridge.ReadTurnControl(root)
	if err != nil {
		t.Fatalf("read turn control: %v", err)
	}
	if len(control.Commands) != 1 {
		t.Fatalf("turn control log = %+v, want one steer", control.Commands)
	}
	got := control.Commands[0]
	if got.Command != bridge.TurnControlSteer || got.TurnID != turnIDs[0] || got.Message != "actually, do this instead" {
		t.Fatalf("control command = %+v, want steer for turn %d", got, turnIDs[0])
	}
}

// Every ↑ press races the queue it was rendered from. A row that has moved on must
// answer 409 with a class the client can act on, never a 500 and never a silent
// success that leaves the user unsure whether their prompt was sent.
func TestSendQueuedTurnConflictsOnARowThatMovedOn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srv, st, session := newRuntimeTestServer(t, ctx, dir)
	turnIDs := stoppedSessionFixture(t, ctx, srv, st, session.ID, "running work", "queued work")

	// A turn id from no queue at all.
	rec := pressSend(t, srv, session.ID, turnIDs[1]+9999)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unknown turn: expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
	// The turn that is executing: it is not a queue row, and Stop is what ends it.
	if rec := pressSend(t, srv, session.ID, turnIDs[0]); rec.Code != http.StatusConflict {
		t.Fatalf("running turn: expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
	// Steering twice from a stale queue: the first delivers, the second finds the row
	// already retired.
	if rec := pressSend(t, srv, session.ID, turnIDs[1]); rec.Code != http.StatusAccepted {
		t.Fatalf("first steer: expected 202, got %d body %s", rec.Code, rec.Body.String())
	}
	rec = pressSend(t, srv, session.ID, turnIDs[1])
	if rec.Code != http.StatusConflict {
		t.Fatalf("second steer: expected 409, got %d body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		ErrorClass string `json:"error_class"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	if response.ErrorClass != "turn_not_sendable" {
		t.Fatalf("error_class = %q, want turn_not_sendable", response.ErrorClass)
	}
}
