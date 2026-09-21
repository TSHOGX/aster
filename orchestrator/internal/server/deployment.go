package server

import (
	"harness-platform/orchestrator/internal/agents"
	"strings"
)

type deploymentResolution struct {
	Mode       string
	DriverID   agents.ID
	DriverSpec agents.DriverSpec
}
type deploymentCapabilityError struct{ code, message string }

func (e *deploymentCapabilityError) Error() string { return e.message }
func capabilityError(code, message string) *deploymentCapabilityError {
	return &deploymentCapabilityError{code: code, message: message}
}
func (s *Server) resolveModeDeployment(mode string) (deploymentResolution, *deploymentCapabilityError) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "agent"
	}

	// "agent" is the only Session mode. A shell is a Terminal, which is a separate
	// Project capability with its own PTY process rather than a Session driver.
	if mode != "agent" {
		return deploymentResolution{}, capabilityError("unsupported_mode", "unsupported mode")
	}
	driver := agents.Pi
	if !s.retainedDrivers[driver] {
		return deploymentResolution{}, capabilityError("driver_disabled", "driver is not enabled in the retained release")
	}
	spec, ok := agents.DriverSpecFor(string(driver))
	if !ok {
		return deploymentResolution{}, capabilityError("driver_disabled", "driver unavailable")
	}
	return deploymentResolution{Mode: mode, DriverID: driver, DriverSpec: spec}, nil
}
