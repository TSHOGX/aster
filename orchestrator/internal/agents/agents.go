package agents

import (
	"fmt"
	"sort"
	"strings"
)

type ID string

// Pi is the only Agent driver. A shell is not an agent: Terminals are a native
// Project capability with their own PTY processes, so there is no shell driver,
// no shell Session mode, and no conversion of terminal bytes into turns.
const Pi ID = "pi"

const (
	PiCodingAgentDir     = "/root/.pi/agent"
	PiSessionDir         = PiCodingAgentDir + "/sessions"
	PiPackageName        = "@earendil-works/pi-coding-agent"
	PiPackageVersion     = "0.77.0"
	PiPackageShasum      = "627664c042507babf8a134a3770285272ccae5d8"
	PiPackageIntegrity   = "sha512-huS+k+dhQRR9PlTK7crLfeSRUw3a96V6JYfP0ZH3Zkko/m10gsYk8dKQmwScSy5Dll516pXorz19BURfD6S2qQ=="
	PiEventSchemaVersion = "pi_rpc_events_v1.0"
	PiBinaryPath         = "/usr/local/bin/pi"
)

type Protocol string

const ProtocolPiRPC Protocol = "pi_rpc"

type DriverKind string

const DriverKindAgent DriverKind = "agent"

type Definition struct {
	ID       ID
	Label    string
	Protocol Protocol
}

// DriverPackageFacts captures the verifiable package identity for a driver as
// it is installed into the agent image.
type DriverPackageFacts struct {
	Name               string
	Version            string
	Shasum             string
	Integrity          string
	EventSchemaVersion string
}

type DriverSpec struct {
	ID                                    ID
	Label                                 string
	Kind                                  DriverKind
	BridgeProtocol                        string
	BridgeProtocolVersion                 int
	TurnInputSchema, OutputSchema         string
	RequiredRuntimeCapabilities           []string
	ModelAccess                           bool
	OutputFormat                          string
	SupportsInterrupt, SupportsCompaction bool
	BinaryPath                            string
	PackageFacts                          DriverPackageFacts
}

var driverSpecs = map[ID]DriverSpec{
	Pi: {ID: Pi, Label: "Pi", Kind: DriverKindAgent, BridgeProtocol: "harness_bridge_v2", BridgeProtocolVersion: 2, TurnInputSchema: "RunTurn", OutputSchema: PiEventSchemaVersion, RequiredRuntimeCapabilities: []string{"exec_stream", "filesystem_rw", "kill", "logs", "network_policy", "stdin"}, ModelAccess: true, OutputFormat: PiEventSchemaVersion, SupportsInterrupt: true, BinaryPath: PiBinaryPath, PackageFacts: DriverPackageFacts{Name: PiPackageName, Version: PiPackageVersion, Shasum: PiPackageShasum, Integrity: PiPackageIntegrity, EventSchemaVersion: PiEventSchemaVersion}},
}

func cloneDriverSpec(s DriverSpec) DriverSpec {
	s.RequiredRuntimeCapabilities = append([]string(nil), s.RequiredRuntimeCapabilities...)
	return s
}
func Lookup(value string) (Definition, bool) {
	spec, ok := DriverSpecFor(value)
	if !ok {
		return Definition{}, false
	}
	return Definition{ID: spec.ID, Label: spec.Label, Protocol: ProtocolPiRPC}, true
}
func DriverSpecFor(value string) (DriverSpec, bool) {
	spec, ok := driverSpecs[ID(strings.TrimSpace(value))]
	if !ok {
		return DriverSpec{}, false
	}
	return cloneDriverSpec(spec), true
}
func AllDriverSpecs() []DriverSpec {
	ids := make([]string, 0, len(driverSpecs))
	for id := range driverSpecs {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	specs := make([]DriverSpec, 0, len(ids))
	for _, id := range ids {
		specs = append(specs, cloneDriverSpec(driverSpecs[ID(id)]))
	}
	return specs
}
func CanonicalDriverID(value string) (ID, error) {
	if ID(strings.TrimSpace(value)) == Pi {
		return Pi, nil
	}
	return "", fmt.Errorf("unsupported driver %q", value)
}
