package execution

import (
	"context"
	"errors"
	"testing"

	"harness-platform/orchestrator/internal/sandbox"
)

type idleFallbackActions struct {
	ReconcileController
	attachment, recovery               error
	recovered                          int
	session, generation, owner, reason string
}

func (a *idleFallbackActions) ReconnectIdle(context.Context, string, string, string) (Binding, error) {
	return Binding{}, idleAttachmentError(a.attachment)
}
func (a *idleFallbackActions) RecoverInterrupted(_ context.Context, session, generation, owner, reason string) error {
	a.recovered++
	a.session, a.generation, a.owner, a.reason = session, generation, owner, reason
	return a.recovery
}

func TestIdleReconciliationEscalatesOnlyUnrecoverableAttachment(t *testing.T) {
	for _, class := range []sandbox.ErrorClass{sandbox.NotFound, sandbox.Precondition, sandbox.Unavailable, sandbox.IdentityChanged, sandbox.Incompatible, sandbox.Capacity, sandbox.Uncertain} {
		t.Run(string(class), func(t *testing.T) {
			original := &sandbox.Error{Class: class, Cause: errors.New("injected")}
			recoveryError := errors.New("recovery drain unavailable")
			actions := &idleFallbackActions{attachment: original, recovery: recoveryError}
			r := Reconciler{Controller: actions, Owner: "owner"}
			err := r.apply(context.Background(), ReconcileIdle, ReconcileWork{SessionID: "session", GenerationID: "generation"})
			if class == sandbox.NotFound || class == sandbox.Precondition {
				if !errors.Is(err, recoveryError) || actions.recovered != 1 || actions.session != "session" || actions.generation != "generation" || actions.owner != "owner" || actions.reason != IdleAttachmentLostReason {
					t.Fatalf("lost process did not retain exact recovery obligation: %+v %v", actions, err)
				}
			} else if actions.recovered != 0 || !errors.Is(err, original) {
				t.Fatalf("transient or incompatible attachment triggered recovery: %+v %v", actions, err)
			}
		})
	}
	if got := idleAttachmentError(context.Canceled); got != context.Canceled {
		t.Fatal("cancellation escalated to cold recovery")
	}
}
