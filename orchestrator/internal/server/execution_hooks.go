package server

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

// ExecutionHooks exposes application projections to the process composition
// root without making the HTTP layer construct a backend or own engine clients.
type ExecutionHooks struct {
	AfterCommit      func(context.Context, bridge.Envelope, int64)
	BeforeComplete   func(context.Context, *store.CompleteTurnParams) error
	AfterEventCommit func(context.Context, int64)
}

func (s *Server) SetRetainedDrivers(ids []string) error {
	drivers := map[agents.ID]bool{}
	for _, id := range ids {
		if id != "pi" && id != "sh" {
			return fmt.Errorf("unsupported retained driver")
		}
		drivers[agents.ID(id)] = true
	}
	if len(drivers) == 0 {
		return fmt.Errorf("retained deployment has no enabled drivers")
	}
	s.retainedDrivers = drivers
	return nil
}

func (s *Server) ExecutionHooks() ExecutionHooks {
	return ExecutionHooks{AfterCommit: s.handleBridgeCommittedEnvelope, BeforeComplete: s.projectBeforeBridgeCompletion, AfterEventCommit: s.publishDurableEvent}
}
func (s *Server) SetNativeStateValidator(validate func(context.Context, store.DriverStateValidationInput, int64) (workspace.File, error)) {
	s.nativeStateValidator = validate
}
