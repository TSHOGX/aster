package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type EnvironmentRepository interface {
	GetEnvironmentBinding(context.Context, string) (Environment, error)
	BeginEnvironmentOperation(context.Context, OperationIntent) (Operation, error)
	RecordEnvironmentOperationOutcome(context.Context, OperationOutcome) error
}

// EnvironmentService executes immutable remote intents after reserving the
// durable operation lane. Backend calls happen outside persistence transactions.
type EnvironmentService struct {
	Repository EnvironmentRepository
	Backend    sandbox.Lifecycle
	Now        func() time.Time
}

// EnvironmentRequest is the immutable remote intent. Configuration accompanies a
// create or configure so the settled outcome records exactly the declaration that
// was dispatched, rather than re-deriving one that may have moved on since.
type EnvironmentRequest struct {
	Reference     sandbox.Reference        `json:"reference"`
	Create        *sandbox.CreateRequest   `json:"create,omitempty"`
	Network       *sandbox.Network         `json:"network,omitempty"`
	Mounts        []sandbox.Mount          `json:"mounts,omitempty"`
	Configuration *GenerationConfiguration `json:"configuration,omitempty"`
}

func (s EnvironmentService) Run(ctx context.Context, intent OperationIntent) (sandbox.Observation, error) {
	if s.Repository == nil || s.Backend == nil {
		return sandbox.Observation{}, fmt.Errorf("environment repository and backend required")
	}
	environment, err := s.Repository.GetEnvironmentBinding(ctx, intent.EnvironmentID)
	if err != nil {
		return sandbox.Observation{}, err
	}
	var request EnvironmentRequest
	decoder := json.NewDecoder(strings.NewReader(string(intent.Request)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return sandbox.Observation{}, fmt.Errorf("decode environment intent: %w", err)
	}
	if err := validateEnvironmentRequest(environment, intent, request); err != nil {
		return sandbox.Observation{}, err
	}
	intent.Now = s.now()
	operation, err := s.Repository.BeginEnvironmentOperation(ctx, intent)
	if err != nil {
		return sandbox.Observation{}, err
	}
	if environment.Fence != intent.ExpectedFence && environment.Fence != operation.Fence {
		return sandbox.Observation{}, ErrStaleOperation
	}
	if operation.State == OperationSucceeded {
		var observed sandbox.Observation
		err := json.Unmarshal(operation.Result, &observed)
		return observed, err
	}
	if operation.State == OperationFailed {
		var result struct {
			ErrorClass sandbox.ErrorClass `json:"error_class"`
		}
		if err := json.Unmarshal(operation.Result, &result); err != nil {
			return sandbox.Observation{}, err
		}
		if result.ErrorClass == "" {
			return sandbox.Observation{}, fmt.Errorf("environment operation previously failed: %s", operation.Error)
		}
		return sandbox.Observation{}, &sandbox.Error{Class: result.ErrorClass, Operation: operation.Kind, Cause: errors.New(operation.Error)}
	}
	if operation.State == OperationPending {
		if err := s.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationIssued, Now: s.now()}); err != nil {
			return sandbox.Observation{}, err
		}
	}
	identity, identityErr := s.Backend.Identity(ctx)
	if identityErr != nil {
		return sandbox.Observation{}, s.recordUncertain(ctx, operation, identityErr)
	}
	if identity.StoreID != environment.StoreID {
		return sandbox.Observation{}, s.recordUncertain(ctx, operation, ErrBackendIdentityChanged)
	}
	var observed sandbox.Observation
	switch operation.Kind {
	case "create":
		observed, err = s.Backend.Create(ctx, *request.Create)
	case "pause":
		observed, err = s.Backend.Pause(ctx, request.Reference)
	case "resume":
		observed, err = s.Backend.Resume(ctx, request.Reference)
	case "configure":
		observed, err = s.Backend.Configure(ctx, request.Reference, *request.Network, request.Mounts)
	default:
		return sandbox.Observation{}, fmt.Errorf("unsupported lifecycle intent %q", operation.Kind)
	}
	if err != nil && sandbox.Is(err, sandbox.Precondition) && (operation.Kind == "pause" || operation.Kind == "resume") {
		// A lost successful reply changes the revision, so the same request now
		// gets 412. Current postcondition evidence settles the original intent;
		// otherwise the old revision is retained and no successor is admitted.
		current, inspectErr := s.Backend.Inspect(ctx, request.Reference)
		if inspectErr == nil && operationPostcondition(operation.Kind, request.Reference, current) {
			observed = current
			err = nil
		}
	}
	if err == nil {
		if observed.StoreID != environment.StoreID || observed.SandboxID == "" || observed.MutationRevision == "" || observed.Epoch == 0 {
			return observed, s.recordUncertain(ctx, operation, fmt.Errorf("backend returned an incomplete or foreign environment observation"))
		}
		if operation.Kind == "create" && (observed.State != "running" || observed.PlacementState != "active") {
			return observed, s.recordUncertain(ctx, operation, fmt.Errorf("created environment has no active placement"))
		}
		if operation.Kind != "create" && !operationPostcondition(operation.Kind, request.Reference, observed) {
			return observed, s.recordUncertain(ctx, operation, fmt.Errorf("backend observation does not establish the requested lifecycle transition"))
		}
		result, encodeErr := json.Marshal(observed)
		if encodeErr != nil {
			return observed, s.recordUncertain(ctx, operation, encodeErr)
		}
		outcome := OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationSucceeded, Result: result, SandboxID: observed.SandboxID, MutationRevision: observed.MutationRevision, Epoch: observed.Epoch, Now: s.now()}
		// Only the dispatched request may become the applied declaration, and only
		// after the engine confirmed the transition it belongs to.
		if request.Configuration != nil && (operation.Kind == "create" || operation.Kind == "configure") {
			applied, encodeErr := json.Marshal(request.Configuration)
			if encodeErr != nil {
				return observed, s.recordUncertain(ctx, operation, encodeErr)
			}
			outcome.Configuration = applied
		}
		commitErr := s.Repository.RecordEnvironmentOperationOutcome(ctx, outcome)
		return observed, commitErr
	}
	if sandbox.Is(err, sandbox.Invalid) || sandbox.Is(err, sandbox.Capacity) {
		class := sandbox.Invalid
		if sandbox.Is(err, sandbox.Capacity) {
			class = sandbox.Capacity
		}
		result, _ := json.Marshal(struct {
			ErrorClass sandbox.ErrorClass `json:"error_class"`
		}{class})
		recordErr := s.Repository.RecordEnvironmentOperationOutcome(ctx, OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationFailed, Result: result, Error: err.Error(), Now: s.now()})
		return observed, errors.Join(err, recordErr)
	}
	return observed, s.recordUncertain(ctx, operation, err)
}

func validateEnvironmentRequest(environment Environment, intent OperationIntent, request EnvironmentRequest) error {
	switch intent.Kind {
	case "create":
		if request.Create == nil || request.Create.IdempotencyKey != intent.IdempotencyKey {
			return fmt.Errorf("create intent must preserve its idempotency key")
		}
		if request.Reference.StoreID != environment.StoreID {
			return ErrBackendIdentityChanged
		}
		if request.Configuration == nil || !sandbox.SameConfiguration(sandbox.Configuration{Network: request.Configuration.Network, Mounts: request.Configuration.Mounts}, request.Create.Network, request.Create.Mounts) {
			return fmt.Errorf("create intent must declare the generation configuration it applies")
		}
	case "pause", "resume", "configure":
		if request.Reference.StoreID != environment.StoreID {
			return ErrBackendIdentityChanged
		}
		if environment.Fence == intent.ExpectedFence && (request.Reference.SandboxID != environment.SandboxID || request.Reference.Epoch != environment.Epoch) {
			return ErrOperationConflict
		}
		// On replay the binding may already hold the successful outcome's newer
		// revision. BeginEnvironmentOperation verifies the original immutable intent.
		if environment.Fence == intent.ExpectedFence && request.Reference.MutationRevision != environment.MutationRevision {
			return ErrOperationConflict
		}
		if intent.Kind == "configure" {
			if request.Network == nil {
				return fmt.Errorf("configuration intent requires a complete network declaration")
			}
			if request.Configuration == nil || !sandbox.SameConfiguration(sandbox.Configuration{Network: request.Configuration.Network, Mounts: request.Configuration.Mounts}, *request.Network, request.Mounts) {
				return fmt.Errorf("configuration intent must declare the generation configuration it applies")
			}
		}
	default:
		return fmt.Errorf("unsupported lifecycle intent %q", intent.Kind)
	}
	return nil
}

func operationPostcondition(kind string, before sandbox.Reference, after sandbox.Observation) bool {
	if before.StoreID != after.StoreID || before.SandboxID != after.SandboxID {
		return false
	}
	switch kind {
	case "pause", "configure":
		return after.State == "paused" && after.PlacementState == "lost" && after.Epoch == before.Epoch
	case "resume":
		return after.State == "running" && after.PlacementState == "active" && after.Epoch == before.Epoch+1
	}
	return false
}
func (s EnvironmentService) recordUncertain(ctx context.Context, operation Operation, cause error) error {
	// A canceled caller cannot cancel persistence of an unknown remote outcome.
	// Failure to record still leaves the issued intent occupying the lane.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.Repository.RecordEnvironmentOperationOutcome(recordCtx, OperationOutcome{ID: operation.ID, Fence: operation.Fence, State: OperationUncertain, Error: cause.Error(), Now: s.now()})
	return errors.Join(cause, err)
}
func (s EnvironmentService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
