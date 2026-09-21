package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/testfixture"
	"path/filepath"
	"testing"
	"time"
)

func emitOutputEnvelope(sessionID, generationID string, turnID int64, sequence int, suffix string) Envelope {
	return Envelope{
		MessageID:    fmt.Sprintf("msg_output_%s_%03d", suffix, sequence),
		RequestID:    fmt.Sprintf("req_output_%s_%03d", suffix, sequence),
		Type:         TypeEmitOutput,
		SessionID:    sessionID,
		GenerationID: generationID,
		TurnID:       &turnID,
		Payload:      json.RawMessage(fmt.Sprintf(`{"output_sequence":%d,"stream":"stdout","payload":{"line":"%s-%03d"}}`, sequence, suffix, sequence)),
	}
}

type storeFailure struct {
	claimErr      error
	completeErr   error
	failGenCalled *bool
}

func (s storeFailure) BridgeHelloAck(context.Context, string, string, string, time.Time, time.Duration) (store.BridgeHelloAck, error) {
	return store.BridgeHelloAck{}, errors.New("unexpected BridgeHelloAck")
}

func (s storeFailure) BridgeProtocolEvidence(context.Context, string, string) (store.BridgeProtocolEvidence, error) {
	return store.BridgeProtocolEvidence{}, errors.New("unexpected BridgeProtocolEvidence")
}

func (s storeFailure) RenewGenerationHeartbeat(context.Context, store.RenewHeartbeatParams) error {
	return errors.New("unexpected RenewGenerationHeartbeat")
}

func (s storeFailure) ClaimNextTurn(context.Context, store.ClaimNextTurnParams) (store.TurnGrant, bool, error) {
	return store.TurnGrant{}, false, s.claimErr
}

func (s storeFailure) ResumeTurn(context.Context, store.ResumeTurnParams) (store.TurnGrant, bool, error) {
	return store.TurnGrant{}, false, errors.New("unexpected ResumeTurn")
}

func (s storeFailure) AckTurnStarted(context.Context, store.AckStartedParams) (int64, error) {
	return 0, errors.New("unexpected AckTurnStarted")
}

func (s storeFailure) CompleteTurn(context.Context, store.CompleteTurnParams) (int64, error) {
	if s.completeErr != nil {
		return 0, s.completeErr
	}
	return 0, errors.New("unexpected CompleteTurn")
}

func (s storeFailure) FailGenerationWithEvent(_ context.Context, _ store.FailGenerationParams) (store.FailGenerationResult, error) {
	if s.failGenCalled != nil {
		*s.failGenCalled = true
		return store.FailGenerationResult{}, nil
	}
	return store.FailGenerationResult{}, errors.New("unexpected FailGeneration")
}

func (s storeFailure) AppendEvent(context.Context, store.AppendEventParams) (int64, error) {
	return 0, errors.New("unexpected AppendEvent")
}

func (s storeFailure) AppendEventsBatch(context.Context, []store.AppendEventParams) ([]store.AppendEventsBatchResult, error) {
	return nil, errors.New("unexpected AppendEventsBatch")
}

func setupBridgeRunningTurn(t *testing.T, ctx context.Context, session string) (*store.Store, execution.Binding, string, int64) {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	if err := st.CreateSession(ctx, store.Session{ID: session, UserID: "lab", Agent: "pi", Status: "created", Workspace: "opaque", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	owner := store.GenerationLeaseOwner("test-owner")
	generation := "gen-" + session
	testfixture.Bind(t, st.DBForTest(), session, generation, owner)
	id, err := st.EnqueueTurn(ctx, session, "prompt", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: session, GenerationID: generation, Owner: owner, RequestID: "claim", LeaseTTL: time.Hour}); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	binding, err := st.GetExecutionBinding(ctx, generation)
	if err != nil {
		t.Fatal(err)
	}
	return st, binding, "", id
}
