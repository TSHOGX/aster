package server

import (
	"fmt"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

// validateNewSession holds the domain admission rules for a session the control
// plane is about to create. These checks used to live inside
// store.CreateSession; they are business rules about what the product accepts,
// not invariants of the sessions table, so they belong on this side of the
// persistence seam.
//
// store.CreateSession still fails closed on the same inputs (mode derivation
// plus the table's CHECK constraints), so moving them here does not widen what
// reaches the database -- it just turns a 500 into a 400 and keeps the rule
// where the policy lives.
func validateNewSession(session store.Session) error {
	if err := sessionstate.Validate(session.Status); err != nil {
		return err
	}
	if _, ok := agents.Lookup(session.Agent); !ok {
		return fmt.Errorf("unsupported driver %q", session.Agent)
	}
	return nil
}
