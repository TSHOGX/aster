package product

import "harness-platform/orchestrator/internal/agentwire"

// AgentPressure is advisory per-attachment telemetry, never ACK authority or
// command completion evidence. No content or credentials are retained here.
func (a *Agents) AgentPressure() []agentwire.Pressure {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]agentwire.Pressure, 0, len(a.attachments))
	for _, attachment := range a.attachments {
		out = append(out, attachment.pressure)
	}
	return out
}
