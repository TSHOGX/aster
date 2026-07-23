package server

// driverDTO is the JSON shape for a single driver in the operator agents
// catalog response (operatorAgentsCatalog).
type driverDTO struct {
	DriverID                    string   `json:"driver_id"`
	Label                       string   `json:"label"`
	Kind                        string   `json:"kind"`
	BridgeProtocol              string   `json:"bridge_protocol"`
	OutputSchema                string   `json:"output_schema"`
	RequiredRuntimeCapabilities []string `json:"required_runtime_capabilities"`
	ModelAccess                 bool     `json:"model_access"`
	SupportsInterrupt           bool     `json:"supports_interrupt"`
	SupportsCompaction          bool     `json:"supports_compaction"`
}

// modeDTO is the JSON shape for a single session mode in the deployment
// capabilities response (deploymentCapabilities).
type modeDTO struct {
	Mode           string  `json:"mode"`
	Label          string  `json:"label"`
	Visible        bool    `json:"visible"`
	CreateEnabled  bool    `json:"create_enabled"`
	DisabledReason *string `json:"disabled_reason"`
}
