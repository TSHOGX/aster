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

	driver := agents.Pi
	if mode == "shell" {
		driver = agents.Shell
	} else if mode != "agent" {
		return deploymentResolution{}, capabilityError("unsupported_mode", "unsupported mode")
	}
	if !s.retainedDrivers[driver] {
		return deploymentResolution{}, capabilityError("driver_disabled", "driver is not enabled in the retained release")
	}
	spec, ok := agents.DriverSpecFor(string(driver))
	if !ok {
		return deploymentResolution{}, capabilityError("driver_disabled", "driver unavailable")
	}
	return deploymentResolution{Mode: mode, DriverID: driver, DriverSpec: spec}, nil
}
