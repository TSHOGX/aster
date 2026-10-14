package evolution

import (
	"context"
	"fmt"
	"time"

	manifest "aster/contracts/volumemanifest"
)

type RunPins struct {
	ConfigurationRevision int64            `json:"configuration_revision"`
	ConfigurationDigest   string           `json:"configuration_digest"`
	SourcePolicyRevision  int64            `json:"source_policy_revision"`
	Template              Revision         `json:"template"`
	Workflow              Revision         `json:"workflow"`
	Service               Revision         `json:"service"`
	Model                 Revision         `json:"model"`
	Tools                 []Revision       `json:"tools"`
	Inputs                []ArtifactRef    `json:"inputs"`
	Base                  ContentRef       `json:"base"`
	Output                OutputBinding    `json:"output"`
	Evaluation            EvaluationPolicy `json:"evaluation"`
}
type Run struct {
	ID              string    `json:"id"`
	GroupID         string    `json:"group_id"`
	BindingID       string    `json:"binding_id"`
	BatchID         string    `json:"batch_id,omitempty"`
	Pins            RunPins   `json:"pins"`
	State           string    `json:"state"`
	Handoff         string    `json:"handoff"`
	EvaluationState string    `json:"evaluation_state"`
	ResultBranch    string    `json:"result_branch"`
	Fence           int64     `json:"fence"`
	Budget          Budget    `json:"budget"`
	RetryOf         string    `json:"retry_of,omitempty"`
	ExperimentOf    string    `json:"experiment_of,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
type Attempt struct {
	ID                string        `json:"id"`
	RunID             string        `json:"run_id"`
	NodeID            string        `json:"node_id"`
	InstanceID        string        `json:"instance_id"`
	ParentInstanceID  string        `json:"parent_instance_id,omitempty"`
	Number            int           `json:"number"`
	Fence             int64         `json:"fence"`
	State             string        `json:"state"`
	Inputs            []ArtifactRef `json:"inputs"`
	EffectivePrompt   *ArtifactRef  `json:"effective_prompt,omitempty"`
	Outputs           []ArtifactRef `json:"outputs"`
	CheckpointID      string        `json:"checkpoint_id,omitempty"`
	ProviderRequestID string        `json:"provider_request_id,omitempty"`
	CostMicros        int64         `json:"cost_micros"`
	Tokens            int64         `json:"tokens"`
	ErrorCode         string        `json:"error_code,omitempty"`
}
type Event struct {
	ID         string        `json:"id"`
	RunID      string        `json:"run_id"`
	AttemptID  string        `json:"attempt_id"`
	ProducerID string        `json:"producer_id"`
	Sequence   int64         `json:"sequence"`
	Fence      int64         `json:"fence"`
	Timestamp  time.Time     `json:"timestamp"`
	Kind       string        `json:"kind"`
	Artifacts  []ArtifactRef `json:"artifacts"`
}
type PatchEntry struct {
	Operation string       `json:"operation"`
	Path      string       `json:"path"`
	Kind      string       `json:"kind"`
	Artifact  *ArtifactRef `json:"artifact,omitempty"`
}
type Patch struct {
	RunID                 string       `json:"run_id"`
	Fence                 int64        `json:"fence"`
	Base                  ContentRef   `json:"base"`
	ReservationGeneration int64        `json:"reservation_generation"`
	Entries               []PatchEntry `json:"entries"`
}

func (p Patch) Validate(output OutputBinding) error {
	if !manifest.ValidID(p.RunID) || p.Fence < 1 || p.ReservationGeneration != output.ReservationGeneration || p.Base.LibraryID != output.LibraryID {
		return fmt.Errorf("patch identity or generation mismatch")
	}
	if err := p.Base.Validate(); err != nil {
		return err
	}
	if err := output.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range p.Entries {
		if err := manifest.ValidAuthoringPath(e.Path, e.Kind == "directory"); err != nil {
			return err
		}
		if !WithinPrefix(output.Prefix, e.Path) || seen[e.Path] {
			return fmt.Errorf("duplicate or unauthorized patch path")
		}
		seen[e.Path] = true
		if e.Kind != "file" && e.Kind != "directory" {
			return fmt.Errorf("unsupported patch entry")
		}
		switch e.Operation {
		case "put":
			if e.Kind == "file" {
				if e.Artifact == nil {
					return fmt.Errorf("file artifact required")
				}
				if err := e.Artifact.Validate(); err != nil {
					return err
				}
			} else if e.Artifact != nil {
				return fmt.Errorf("directory cannot carry bytes")
			}
		case "remove":
			if e.Artifact != nil {
				return fmt.Errorf("remove cannot carry bytes")
			}
		default:
			return fmt.Errorf("unsupported patch operation")
		}
	}
	return nil
}

type CheckRequirement struct {
	Digest            string      `json:"digest"`
	PolicyID          string      `json:"policy_id"`
	PolicyRevision    int64       `json:"policy_revision"`
	Path              string      `json:"path"`
	Mode              string      `json:"mode"`
	Profile           Revision    `json:"profile"`
	Service           Revision    `json:"service"`
	ReferenceStrategy string      `json:"reference_strategy"`
	Reference         *ContentRef `json:"reference,omitempty"`
}
type PreparedIdentity struct {
	LibraryID      string             `json:"library_id"`
	Main           ContentRef         `json:"main"`
	Sources        []ContentRef       `json:"sources"`
	Result         ContentRef         `json:"result"`
	PolicyRevision int64              `json:"policy_revision"`
	PolicyDigest   string             `json:"policy_digest"`
	Checks         []CheckRequirement `json:"checks"`
}
type Branch struct {
	ID            string     `json:"id"`
	LibraryID     string     `json:"library_id"`
	Name          string     `json:"name"`
	Head          ContentRef `json:"head"`
	InitialBase   ContentRef `json:"initial_base"`
	DraftRevision int64      `json:"draft_revision"`
	RunID         string     `json:"run_id,omitempty"`
}
type MergeRequest struct {
	ID                     string            `json:"id"`
	LibraryID              string            `json:"library_id"`
	BranchIDs              []string          `json:"branch_ids"`
	State                  string            `json:"state"`
	PreparedDigest         string            `json:"prepared_digest,omitempty"`
	Identity               *PreparedIdentity `json:"identity,omitempty"`
	AcceptedCommit         string            `json:"accepted_commit,omitempty"`
	PublicationOperationID string            `json:"publication_operation_id,omitempty"`
}
type Review struct {
	ID             string    `json:"id"`
	PreparedDigest string    `json:"prepared_digest"`
	Reviewer       Principal `json:"reviewer"`
	Verdict        string    `json:"verdict"`
	Rationale      string    `json:"rationale"`
	CreatedAt      time.Time `json:"created_at"`
}
type EvaluationSubject struct {
	Candidate         ContentRef  `json:"candidate"`
	PreparedDigest    string      `json:"prepared_digest"`
	RequirementDigest string      `json:"requirement_digest"`
	Reference         *ContentRef `json:"reference,omitempty"`
	Profile           Revision    `json:"profile"`
	Dataset           ArtifactRef `json:"dataset"`
	Scorer            Revision    `json:"scorer"`
	Runtime           Revision    `json:"runtime"`
	Model             Revision    `json:"model"`
	Tools             []Revision  `json:"tools"`
	Seed              int64       `json:"seed"`
}
type Metric struct {
	Name     string  `json:"name"`
	Unit     string  `json:"unit"`
	Value    float64 `json:"value"`
	Samples  int64   `json:"samples"`
	Coverage float64 `json:"coverage"`
}
type EvaluationResult struct {
	JobID                string            `json:"job_id"`
	Service              Revision          `json:"service"`
	Fence                int64             `json:"fence"`
	Subject              EvaluationSubject `json:"subject"`
	State                string            `json:"state"`
	Outcome              string            `json:"outcome"`
	Metrics              []Metric          `json:"metrics"`
	Reports              []ArtifactRef     `json:"reports"`
	ReproducibilityLimit string            `json:"reproducibility_limit,omitempty"`
}
type JobSubmission struct {
	RequestID string    `json:"request_id"`
	RunID     string    `json:"run_id"`
	Fence     int64     `json:"fence"`
	GrantID   string    `json:"grant_id"`
	Pins      RunPins   `json:"pins"`
	Budget    Budget    `json:"budget"`
	Deadline  time.Time `json:"deadline"`
}
type JobStatus struct {
	JobID                  string `json:"job_id"`
	State                  string `json:"state"`
	Fence                  int64  `json:"fence"`
	LastSequence           int64  `json:"last_sequence"`
	ReconciliationRequired bool   `json:"reconciliation_required"`
}
type JobResult struct {
	JobID            string        `json:"job_id"`
	WinningAttemptID string        `json:"winning_attempt_id"`
	Fence            int64         `json:"fence"`
	Patch            Patch         `json:"patch"`
	CostMicros       int64         `json:"cost_micros"`
	Artifacts        []ArtifactRef `json:"artifacts"`
}
type EventPage struct {
	Events        []Event    `json:"events"`
	NextSequence  int64      `json:"next_sequence"`
	CursorExpired bool       `json:"cursor_expired"`
	Snapshot      *JobStatus `json:"snapshot,omitempty"`
}
type EvolutionProvider interface {
	DescribeCapabilities(context.Context) (Capabilities, error)
	Submit(context.Context, JobSubmission) (JobStatus, error)
	GetStatus(context.Context, string) (JobStatus, error)
	Cancel(context.Context, string, int64) error
	ReadEvents(context.Context, string, int64) (EventPage, error)
	GetResult(context.Context, string) (JobResult, error)
}
type EvaluationSubmission struct {
	RequestID  string            `json:"request_id"`
	JobID      string            `json:"job_id"`
	Fence      int64             `json:"fence"`
	GrantID    string            `json:"grant_id"`
	Subject    EvaluationSubject `json:"subject"`
	Budget     Budget            `json:"budget"`
	Thresholds []MetricThreshold `json:"thresholds"`
}
type EvaluationProvider interface {
	DescribeCapabilities(context.Context) (Capabilities, error)
	SubmitEvaluation(context.Context, EvaluationSubmission) (JobStatus, error)
	GetStatus(context.Context, string) (JobStatus, error)
	Cancel(context.Context, string, int64) error
	GetEvaluation(context.Context, string) (EvaluationResult, error)
}
