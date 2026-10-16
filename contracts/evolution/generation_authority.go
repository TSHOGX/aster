package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"encoding/json"
	"fmt"
)

type GenerationAssignment struct {
	BatchID      string                `json:"batch_id,omitempty"`
	RunID        string                `json:"run_id"`
	GroupID      string                `json:"group_id"`
	BindingID    string                `json:"binding_id"`
	OccurrenceID string                `json:"occurrence_id"`
	Requester    Principal             `json:"requester"`
	Automatic    bool                  `json:"automatic"`
	Pins         RunPins               `json:"pins"`
	Budget       Budget                `json:"budget"`
	Experiment   *GenerationExperiment `json:"experiment,omitempty"`
}

// GenerationExperiment binds explicit checkpoint reuse to an admitted source.
// The trusted execution service verifies the sealed checkpoint and semantic
// reuse plan; central authority checks lineage, current access and shared budget.
type GenerationExperiment struct {
	Source          GenerationGrant `json:"source"`
	SourceFence     int64           `json:"source_fence"`
	CheckpointID    string          `json:"checkpoint_id"`
	CheckpointFence int64           `json:"checkpoint_fence"`
	Checkpoint      ArtifactRef     `json:"checkpoint"`
	StartNode       string          `json:"start_node"`
}

func (a GenerationAssignment) Digest() (string, error) {
	if a.BatchID != "" && !manifest.ValidID(a.BatchID) {
		return "", fmt.Errorf("invalid batch identity")
	}
	for _, id := range []string{a.RunID, a.GroupID, a.BindingID, a.OccurrenceID, a.Requester.Subject} {
		if !manifest.ValidID(id) {
			return "", fmt.Errorf("invalid generation identity")
		}
	}
	if a.Requester.Kind != "human" || a.Requester.Issuer == "" || a.Budget.Validate() != nil || a.Pins.Base.Validate() != nil || a.Pins.Output.Validate() != nil {
		return "", fmt.Errorf("invalid generation pins")
	}
	if e := a.Experiment; e != nil {
		if a.Automatic || e.Source.Validate() != nil || e.Source.RunID == a.RunID || e.Source.GroupID != a.GroupID || e.SourceFence < 1 || e.CheckpointFence < 1 || e.CheckpointFence >= e.SourceFence || !manifest.ValidID(e.CheckpointID) || !manifest.ValidID(e.StartNode) || e.Checkpoint.Validate() != nil || e.Checkpoint.GroupID != a.GroupID || e.Checkpoint.GrantID != e.Source.ID || a.Pins.Workflow.Validate() != nil {
			return "", fmt.Errorf("invalid experiment lineage")
		}
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > MaxDocumentBytes {
		return "", fmt.Errorf("invalid generation request size")
	}
	return Digest(raw), nil
}

type GenerationGrant struct {
	ID               string `json:"id"`
	RunID            string `json:"run_id"`
	GroupID          string `json:"group_id"`
	AssignmentDigest string `json:"assignment_digest"`
}

func (g GenerationGrant) Validate() error {
	if !manifest.ValidID(g.ID) || !manifest.ValidID(g.RunID) || !manifest.ValidID(g.GroupID) || !manifest.ValidDigest(g.AssignmentDigest) {
		return fmt.Errorf("invalid generation grant")
	}
	return nil
}

type GenerationGrantScope struct {
	Grant  GenerationGrant `json:"grant"`
	Action string          `json:"action"`
	Fence  int64           `json:"fence"`
}
type GenerationObservation struct {
	Grant GenerationGrant `json:"grant"`
	Fence int64           `json:"fence"`
	State string          `json:"state"`
}

// GenerationDependency retains the exact successful upstream output selected for
// one input port. Batch/configuration and current authority are checked centrally.
type GenerationDependency struct {
	InputPort  string      `json:"input_port"`
	BindingID  string      `json:"binding_id"`
	RunID      string      `json:"run_id"`
	OutputPort string      `json:"output_port"`
	Fence      int64       `json:"fence"`
	Artifact   ArtifactRef `json:"artifact"`
}
type GenerationOutputScope struct {
	GroupID string `json:"group_id"`
	RunID   string `json:"run_id"`
	Port    string `json:"port"`
	Fence   int64  `json:"fence"`
}

func (s GenerationOutputScope) Validate() error {
	if !manifest.ValidID(s.GroupID) || !manifest.ValidID(s.RunID) || !manifest.ValidID(s.Port) || s.Fence < 1 {
		return fmt.Errorf("invalid output scope")
	}
	return nil
}

type GenerationOutput struct {
	Scope    GenerationOutputScope `json:"scope"`
	Workflow Revision              `json:"workflow"`
	Artifact ArtifactRef           `json:"artifact"`
}

// GenerationAdmissionCancellation closes a queued request even when the caller
// never received its admission receipt. It cannot authorize execution.
type GenerationAdmissionCancellation struct {
	Assignment GenerationAssignment `json:"assignment"`
	Fence      int64                `json:"fence"`
}
