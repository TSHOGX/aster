package server

import (
	"harness-platform/orchestrator/internal/agents"

	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/store"
)

func TestAckCompletionOutcome(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantStatus string
		wantClass  string
		wantOK     bool
	}{
		{
			name:       "failed with a transient class",
			payload:    `{"status":"failed","error_class":"upstream_rate_limit"}`,
			wantStatus: "failed",
			wantClass:  "upstream_rate_limit",
			wantOK:     true,
		},
		{
			name:       "absent status means completed",
			payload:    `{}`,
			wantStatus: "completed",
			wantClass:  "",
			wantOK:     true,
		},
		{
			name:       "successful completion",
			payload:    `{"status":"completed"}`,
			wantStatus: "completed",
			wantOK:     true,
		},
		{
			name:    "unparseable payload is not acted on",
			payload: `not json`,
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, class, ok := ackCompletionOutcome(bridge.Envelope{
				Payload: json.RawMessage(tc.payload),
			})
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if class != tc.wantClass {
				t.Fatalf("error class = %q, want %q", class, tc.wantClass)
			}
		})
	}
}

// The resumable set is a safety boundary, so it is pinned explicitly rather than
// left to whatever the map happens to contain.
func TestResumableErrorClasses(t *testing.T) {
	resumable := []string{
		"upstream_rate_limit",
		"proxy_context_lost",
		"upstream_timeout",
		"upstream_5xx",
	}
	for _, class := range resumable {
		if !resumableErrorClasses[class] {
			t.Errorf("%q should be resumable: it is a fault on the path to the model, not in the agent's work", class)
		}
	}

	// Each of these must NOT be resumed, for a specific reason.
	notResumable := map[string]string{
		"agent_prompt_rejected":            "the pi session history is already malformed; a continuation hits the same rejection",
		"session_destroyed":                "there is no sandbox left to continue into",
		"sandbox_heartbeat_stalled_manual": "the sandbox is gone; a continuation would queue against a dead generation",
		"unknown_after_ack_started":        "the outcome is unknown, so continuing risks repeating side effects",
		"upstream_auth":                    "credentials are wrong; retrying cannot fix it and only burns budget",
		"":                                 "an empty class carries no evidence that the fault was transient",
	}
	for class, why := range notResumable {
		if resumableErrorClasses[class] {
			t.Errorf("%q must not be resumable: %s", class, why)
		}
	}
}

// resumeRecorder captures whether the resume path reached the store, which is the
// part unit-testing the decision cannot prove. ResumeFailedTurn's own guards are
// covered against a real database in internal/store/turn_resume_test.go.
type resumeRecorder struct {
	serverStore
	mu     sync.Mutex
	calls  []store.ResumeFailedTurnParams
	result store.ResumeFailedTurnResult
	err    error
}

func (r *resumeRecorder) ResumeFailedTurn(_ context.Context, p store.ResumeFailedTurnParams) (store.ResumeFailedTurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, p)
	return r.result, r.err
}

func (r *resumeRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *resumeRecorder) firstCall() (store.ResumeFailedTurnParams, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return store.ResumeFailedTurnParams{}, false
	}
	return r.calls[0], true
}

func resumeTestServer(t *testing.T, resume config.TurnResumeConfig) (*Server, *resumeRecorder) {
	t.Helper()
	recorder := &resumeRecorder{result: store.ResumeFailedTurnResult{TurnID: 99, Attempt: 1}}
	srv := &Server{retainedDrivers: map[agents.ID]bool{agents.Pi: true, agents.Shell: true},
		store: recorder,
		hub:   events.NewHub(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: config.Config{
			Harness: config.HarnessConfig{
				Bridge: config.BridgeConfig{TurnResume: resume},
			},
		},
	}
	return srv, recorder
}

func failedAck(turnID *int64, errorClass string) bridge.Envelope {
	return bridge.Envelope{
		Type:         bridge.TypeAckTurnCompleted,
		SessionID:    "sess_1",
		GenerationID: "gen_1",
		TurnID:       turnID,
		Payload: json.RawMessage(
			`{"status":"failed","error_class":"` + errorClass + `"}`),
	}
}

// waitForCalls polls because the resume runs on its own goroutine, detached so it
// can outlive the bridge request context.
func waitForCalls(recorder *resumeRecorder, want int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.callCount() >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return recorder.callCount() >= want
}

// The wiring: a transient failure must actually reach the store, with the
// continuation prompt and the configured budget. This is where a feature like
// this silently does nothing.
func TestMaybeResumeFailedTurnReachesTheStore(t *testing.T) {
	srv, recorder := resumeTestServer(t, config.TurnResumeConfig{Enabled: true, MaxAttempts: 2})
	turnID := int64(7)

	srv.maybeResumeFailedTurn(context.Background(), failedAck(&turnID, "upstream_rate_limit"))

	if !waitForCalls(recorder, 1) {
		t.Fatal("transient turn failure never reached ResumeFailedTurn")
	}
	call, _ := recorder.firstCall()
	if call.SessionID != "sess_1" || call.TurnID != turnID {
		t.Fatalf("unexpected resume target: %+v", call)
	}
	if call.MaxAttempts != 2 {
		t.Fatalf("MaxAttempts = %d, want the configured 2", call.MaxAttempts)
	}
	if strings.TrimSpace(call.Content) == "" {
		t.Fatal("resume must carry a continuation prompt")
	}
}

// A non-transient failure must be left alone: continuing an already-malformed pi
// session just reproduces the same rejection.
func TestMaybeResumeFailedTurnIgnoresNonTransientFailure(t *testing.T) {
	srv, recorder := resumeTestServer(t, config.TurnResumeConfig{Enabled: true, MaxAttempts: 2})
	turnID := int64(7)

	srv.maybeResumeFailedTurn(context.Background(), failedAck(&turnID, "agent_prompt_rejected"))

	time.Sleep(150 * time.Millisecond)
	if recorder.callCount() != 0 {
		t.Fatalf("agent_prompt_rejected must not be resumed, got %d calls", recorder.callCount())
	}
}

// A successful turn is not a candidate.
func TestMaybeResumeFailedTurnIgnoresSuccess(t *testing.T) {
	srv, recorder := resumeTestServer(t, config.TurnResumeConfig{Enabled: true, MaxAttempts: 2})
	turnID := int64(7)

	srv.maybeResumeFailedTurn(context.Background(), bridge.Envelope{
		Type:         bridge.TypeAckTurnCompleted,
		SessionID:    "sess_1",
		GenerationID: "gen_1",
		TurnID:       &turnID,
		Payload:      json.RawMessage(`{"status":"completed"}`),
	})

	time.Sleep(150 * time.Millisecond)
	if recorder.callCount() != 0 {
		t.Fatalf("a completed turn must not be resumed, got %d calls", recorder.callCount())
	}
}

// With the feature off nothing may happen, so it can be rolled out deliberately.
func TestMaybeResumeFailedTurnDisabledDoesNothing(t *testing.T) {
	srv, recorder := resumeTestServer(t, config.TurnResumeConfig{Enabled: false, MaxAttempts: 2})
	turnID := int64(7)

	srv.maybeResumeFailedTurn(context.Background(), failedAck(&turnID, "upstream_rate_limit"))

	time.Sleep(150 * time.Millisecond)
	if recorder.callCount() != 0 {
		t.Fatalf("disabled turn resume must do nothing, got %d calls", recorder.callCount())
	}
}

// A declined resume is an expected outcome and must not panic or retry.
func TestMaybeResumeFailedTurnToleratesDeclinedResume(t *testing.T) {
	srv, recorder := resumeTestServer(t, config.TurnResumeConfig{Enabled: true, MaxAttempts: 2})
	recorder.err = store.ErrTurnNotResumable
	turnID := int64(7)

	srv.maybeResumeFailedTurn(context.Background(), failedAck(&turnID, "upstream_rate_limit"))

	if !waitForCalls(recorder, 1) {
		t.Fatal("resume was never attempted")
	}
	time.Sleep(100 * time.Millisecond)
	if recorder.callCount() != 1 {
		t.Fatalf("a declined resume must not be retried, got %d calls", recorder.callCount())
	}
}

// An envelope with no turn id carries nothing to resume.
func TestMaybeResumeFailedTurnRequiresTurnID(t *testing.T) {
	srv, recorder := resumeTestServer(t, config.TurnResumeConfig{Enabled: true, MaxAttempts: 2})

	srv.maybeResumeFailedTurn(context.Background(), failedAck(nil, "upstream_rate_limit"))

	time.Sleep(150 * time.Millisecond)
	if recorder.callCount() != 0 {
		t.Fatalf("an envelope without a turn id must be ignored, got %d calls", recorder.callCount())
	}
}
