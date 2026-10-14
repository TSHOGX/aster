package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	manifest "aster/contracts/volumemanifest"
)

const ProtocolVersion = 1

type ContentRef struct {
	LibraryID  string `json:"library_id"`
	CommitID   string `json:"commit_id"`
	Profile    string `json:"profile"`
	TreeDigest string `json:"tree_digest"`
}

func (r ContentRef) Validate() error {
	if !manifest.ValidID(r.LibraryID) || !ObjectID(r.CommitID) || !manifest.SupportedProfile(r.Profile) || !manifest.ValidDigest(r.TreeDigest) {
		return fmt.Errorf("invalid content reference")
	}
	return nil
}
func ObjectID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type ReleaseRef struct {
	PublisherID      string `json:"publisher_id"`
	LibraryID        string `json:"library_id"`
	VersionID        string `json:"version_id"`
	DescriptorDigest string `json:"descriptor_digest"`
}
type ArtifactRef struct {
	ID             string `json:"id"`
	GroupID        string `json:"group_id"`
	SchemaDigest   string `json:"schema_digest"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	MediaType      string `json:"media_type"`
	GrantID        string `json:"grant_id"`
	PolicyRevision int64  `json:"policy_revision"`
}

func (a ArtifactRef) Validate() error {
	if !manifest.ValidID(a.ID) || !manifest.ValidID(a.GroupID) || !manifest.ValidID(a.GrantID) || !manifest.ValidDigest(a.Digest) || !manifest.ValidDigest(a.SchemaDigest) || a.Size < 0 || a.PolicyRevision < 1 || a.MediaType == "" {
		return fmt.Errorf("invalid scoped artifact")
	}
	return nil
}

type Capabilities struct {
	ProtocolVersion         int      `json:"protocol_version"`
	WorkflowVersions        []int    `json:"workflow_versions"`
	NodeKinds               []string `json:"node_kinds"`
	EditableGraph           bool     `json:"editable_graph"`
	NodeTrace               bool     `json:"node_trace"`
	EffectivePrompt         bool     `json:"effective_prompt"`
	PauseResume             bool     `json:"pause_resume"`
	FixtureDryRun           bool     `json:"fixture_dry_run"`
	DeterministicTransforms bool     `json:"deterministic_transforms"`
	ArtifactSignatures      bool     `json:"artifact_signatures"`
	VersionedModels         bool     `json:"versioned_models"`
}
type Service struct {
	Ref                  Revision     `json:"ref"`
	Kind                 string       `json:"kind"`
	ConnectionID         string       `json:"connection_id"`
	RunnerDigest         string       `json:"runner_digest"`
	CredentialRef        string       `json:"credential_ref"`
	CredentialGeneration int64        `json:"credential_generation"`
	Capabilities         Capabilities `json:"capabilities"`
	Enabled              bool         `json:"enabled"`
}

func (s Service) Validate() error {
	if err := s.Ref.Validate(); err != nil {
		return err
	}
	if s.Kind != "evolution" && s.Kind != "evaluation" {
		return fmt.Errorf("invalid service kind")
	}
	if !manifest.ValidID(s.ConnectionID) || !manifest.ValidDigest(s.RunnerDigest) || s.Capabilities.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("invalid service capabilities or connection")
	}
	return nil
}

type Template struct {
	Ref              Revision          `json:"ref"`
	Goal             string            `json:"goal"`
	ParametersSchema json.RawMessage   `json:"parameters_schema"`
	Inputs           map[string]string `json:"inputs"`
	Outputs          map[string]string `json:"outputs"`
	Profile          string            `json:"profile"`
	Workflows        []Revision        `json:"workflows"`
	SuggestedPrefix  string            `json:"suggested_prefix"`
	Evaluation       EvaluationPolicy  `json:"evaluation"`
}
type Budget struct {
	MaxCostMicros    int64 `json:"max_cost_micros"`
	MaxTokens        int64 `json:"max_tokens"`
	MaxAttempts      int   `json:"max_attempts"`
	TimeoutSeconds   int   `json:"timeout_seconds"`
	MaxArtifactBytes int64 `json:"max_artifact_bytes"`
}

func (b Budget) Validate() error {
	if b.MaxCostMicros < 0 || b.MaxTokens < 1 || b.MaxAttempts < 1 || b.TimeoutSeconds < 1 || b.MaxArtifactBytes < 1 {
		return fmt.Errorf("finite nonnegative budget required")
	}
	return nil
}

type EvaluationPolicy struct {
	Mode              string      `json:"mode"`
	Profile           *Revision   `json:"profile,omitempty"`
	Service           *Revision   `json:"service,omitempty"`
	ReferenceStrategy string      `json:"reference_strategy"`
	ReferenceRelease  *ReleaseRef `json:"reference_release,omitempty"`
}

func (p EvaluationPolicy) Validate() error {
	if p.Mode == "disabled" {
		if p.Profile != nil || p.Service != nil || p.ReferenceRelease != nil || p.ReferenceStrategy != "none" {
			return fmt.Errorf("disabled evaluation cannot carry hidden checks")
		}
		return nil
	}
	if (p.Mode != "report_only" && p.Mode != "required") || p.Profile == nil || p.Service == nil {
		return fmt.Errorf("evaluation mode, profile and service required")
	}
	if err := p.Profile.Validate(); err != nil {
		return err
	}
	if err := p.Service.Validate(); err != nil {
		return err
	}
	switch p.ReferenceStrategy {
	case "none", "generation_base", "target_main":
		if p.ReferenceRelease != nil {
			return fmt.Errorf("unexpected reference release")
		}
	case "pinned_release":
		if p.ReferenceRelease == nil {
			return fmt.Errorf("pinned release required")
		}
		if err := p.ReferenceRelease.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid reference strategy")
	}
	return nil
}

type MetricThreshold struct {
	Name           string  `json:"name"`
	Unit           string  `json:"unit"`
	Operator       string  `json:"operator"`
	Value          float64 `json:"value"`
	MinimumSamples int64   `json:"minimum_samples"`
}
type EvaluationProfile struct {
	Ref                Revision          `json:"ref"`
	Dataset            ArtifactRef       `json:"dataset"`
	Scorer             Revision          `json:"scorer"`
	Runtime            Revision          `json:"runtime"`
	Model              Revision          `json:"model"`
	Tools              []Revision        `json:"tools"`
	Thresholds         []MetricThreshold `json:"thresholds"`
	FixedModelRequired bool              `json:"fixed_model_required"`
}
type OutputBinding struct {
	LibraryID             string `json:"library_id"`
	Prefix                string `json:"prefix"`
	Profile               string `json:"profile"`
	ReservationGeneration int64  `json:"reservation_generation"`
}

func (o OutputBinding) Validate() error {
	if !manifest.ValidID(o.LibraryID) || !manifest.SupportedProfile(o.Profile) || o.ReservationGeneration < 1 {
		return fmt.Errorf("invalid output reservation")
	}
	return manifest.ValidPath(o.Prefix, true)
}
func WithinPrefix(prefix, p string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
func PrefixesOverlap(a, b string) bool   { return WithinPrefix(a, b) || WithinPrefix(b, a) }

type SourceBinding struct {
	ID                    string   `json:"id"`
	Kind                  string   `json:"kind"`
	ResourceID            string   `json:"resource_id"`
	PolicyRevision        int64    `json:"policy_revision"`
	AudienceGroups        []string `json:"audience_groups"`
	DerivativePublication bool     `json:"derivative_publication"`
	Exclusions            []string `json:"exclusions"`
	CaptureFrontier       string   `json:"capture_frontier"`
}
type Binding struct {
	ID         string           `json:"id"`
	Enabled    bool             `json:"enabled"`
	Template   Revision         `json:"template"`
	Workflow   Revision         `json:"workflow"`
	Service    Revision         `json:"service"`
	Parameters json.RawMessage  `json:"parameters"`
	SourceIDs  []string         `json:"source_ids"`
	Output     OutputBinding    `json:"output"`
	Evaluation EvaluationPolicy `json:"evaluation"`
	Budget     Budget           `json:"budget"`
	Trigger    string           `json:"trigger"`
	Schedule   string           `json:"schedule,omitempty"`
	DependsOn  []string         `json:"depends_on"`
}
type Configuration struct {
	GroupID     string          `json:"group_id"`
	Revision    int64           `json:"revision"`
	AutoEnabled bool            `json:"auto_enabled"`
	Sources     []SourceBinding `json:"sources"`
	Bindings    []Binding       `json:"bindings"`
	Budget      Budget          `json:"budget"`
}
type Activation struct {
	OperationID         string              `json:"operation_id"`
	GroupID             string              `json:"group_id"`
	DesiredRevision     int64               `json:"desired_revision"`
	ActiveRevision      int64               `json:"active_revision"`
	ConfigurationDigest string              `json:"configuration_digest"`
	State               string              `json:"state"`
	Receipts            []ActivationReceipt `json:"receipts"`
}
type ActivationReceipt struct {
	Participant         string `json:"participant"`
	OperationID         string `json:"operation_id"`
	ConfigurationDigest string `json:"configuration_digest"`
	Generation          int64  `json:"generation"`
	State               string `json:"state"`
}

// Authority is consulted for each read and effect. Stored revisions describe
// provenance; they are never an authorization cache.
type Principal struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Kind    string `json:"kind"`
}
type AccessRequest struct {
	Principal  Principal `json:"principal"`
	Action     string    `json:"action"`
	GroupID    string    `json:"group_id"`
	LibraryID  string    `json:"library_id,omitempty"`
	ResourceID string    `json:"resource_id,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	GrantID    string    `json:"grant_id,omitempty"`
	Fence      int64     `json:"fence,omitempty"`
}
type AccessDecision struct {
	Allowed        bool   `json:"allowed"`
	PolicyRevision int64  `json:"policy_revision"`
	Reason         string `json:"reason"`
}
type Authority interface {
	Authorize(context.Context, AccessRequest) (AccessDecision, error)
}
type RuntimeAllocation struct {
	RequestID string        `json:"request_id"`
	Principal Principal     `json:"principal"`
	GroupID   string        `json:"group_id"`
	RunID     string        `json:"run_id"`
	Fence     int64         `json:"fence"`
	Budget    Budget        `json:"budget"`
	Inputs    []ArtifactRef `json:"inputs"`
	Runtime   Revision      `json:"runtime"`
}
type RuntimeLease struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}
type RuntimeAuthority interface {
	Allocate(context.Context, RuntimeAllocation) (RuntimeLease, error)
	Reconcile(context.Context, string) (RuntimeLease, error)
	Release(context.Context, string) error
}
