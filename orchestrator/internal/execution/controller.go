package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/sandbox"

	"github.com/google/uuid"
)

type ControllerRepository interface {
	SessionRetirementRepository
	InspectionRepository
	AdoptionRepository
	TurnCommandRepository
	AssertExecutionOwner(context.Context, string) error
	ExecutionSession(context.Context, string) (Session, error)
	BeginSessionProvisioning(context.Context, OperationIntent, string, int) (Operation, error)
	BeginInitialExecutionResume(context.Context, string, string, int) (Operation, error)
	BeginWorkspaceProvisionPause(context.Context, string, string) (Operation, error)
	AssertExecutionAccepted(context.Context, string, string, time.Time) error
	EnvironmentRepository
	StorageRepository
	WorkerRepository
	IdleReleaseRepository
	RetainedResumeRepository
	InterruptedRecoveryRepository
	CaptureMaintenanceRepository
	PinBackendIdentity(context.Context, BackendIdentity) error
	BeginGenerationConfiguration(context.Context, string, string, GenerationConfiguration) (Operation, error)
	CreateEnvironmentBinding(context.Context, Environment) error
	FindSessionEnvironment(context.Context, string) (Environment, bool, error)
	FindActiveExecutionBinding(context.Context, string) (Binding, bool, error)
	FindLastExecutionBinding(context.Context, string) (Binding, bool, error)
}
type ControllerBackend interface {
	sandbox.Lifecycle
	sandbox.Processes
	VolumeBackend
}

type LaunchPlan struct {
	TemplateID, SpecDigest, ManifestDigest                          string
	CPUMillis, MemoryBytes, DiskBytes, WorkspaceBytes, JournalBytes int64
	Network                                                         sandbox.Network
	Mounts                                                          []sandbox.Mount
	Environment                                                     []string
}
type PlanSource interface {
	Plan(context.Context, Session, string) (LaunchPlan, error)
}

// desiredEnvironment is the environment's IDENTITY. It holds only what cannot
// change while a retained environment keeps its overlay and volumes: the driver,
// the pinned template release, and the resource and storage caps. It is compared
// exactly, so a mismatch means a different environment rather than a change.
//
// Network, content mounts and launch environment are deliberately absent. Those
// are generation configuration: publishing a new asset revision or reserving a
// new service destination must be adoptable by an existing session, and folding
// them into identity would make every retained environment conflict forever.
type desiredEnvironment struct {
	Driver, Template, ManifestDigest      string
	CPU, Memory, Disk, Workspace, Journal int64
}

// GenerationConfiguration is the declaration one execution runs under. It is
// re-derived per generation, recorded with its own digest, and applied to a
// retained paused environment through a fenced configure operation.
type GenerationConfiguration struct {
	SpecDigest  string          `json:"spec_digest"`
	Network     sandbox.Network `json:"network"`
	Mounts      []sandbox.Mount `json:"mounts"`
	Environment []string        `json:"environment"`
}

type Controller struct {
	Repository        ControllerRepository
	Backend           ControllerBackend
	Agents            AgentLauncher
	Plans             PlanSource
	BackendName       string
	LeaseTTL          time.Duration
	MaxActive         int
	Lifetime          context.Context
	Routes            RouteRevoker
	GrantRevoker      GrantRevoker
	RecoveryReader    JournalRecoveryReader
	RecoveryProjector RecoveryProjector
	NativeRestorer    NativeRestorer
	mu                sync.Mutex
	closed            bool
	runContext        context.Context
	cancel            context.CancelFunc
	sessions          map[string]*sessionWorker
}
type sessionWorker struct {
	mu         sync.Mutex
	binding    Binding
	connection AgentConnection
}

// Ensure owns one session's provision/start path. Per-session serialization is
// an optimization around the durable environment lane and generation fence, not
// a replacement for them. A persisted execution after restart requires explicit
// adoption rather than an implicit second process or lost-history cold start.
func (c *Controller) Ensure(ctx context.Context, session Session, owner string) (Binding, error) {
	if c.Repository == nil || c.Backend == nil || c.Agents == nil || c.Plans == nil || c.Lifetime == nil || c.BackendName == "" || c.LeaseTTL <= 0 {
		return Binding{}, fmt.Errorf("complete execution controller configuration required")
	}
	if session.ID == "" || owner == "" {
		return Binding{}, fmt.Errorf("session and lifecycle owner required")
	}
	worker, lifetime, err := c.session(session.ID)
	if err != nil {
		return Binding{}, err
	}
	requestContext, cancelRequest := context.WithCancel(ctx)
	stopShutdown := context.AfterFunc(lifetime, cancelRequest)
	defer func() { stopShutdown(); cancelRequest() }()
	ctx = requestContext
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if err := c.Repository.AssertSessionNotRetiring(ctx, session.ID); err != nil {
		return Binding{}, err
	}

	if err := c.Repository.AssertExecutionOwner(ctx, owner); err != nil {
		return Binding{}, err
	}
	identity, err := c.Backend.Identity(ctx)
	if err != nil {
		return Binding{}, err
	}
	if err := c.Repository.PinBackendIdentity(ctx, BackendIdentity{Name: c.BackendName, StoreID: identity.StoreID, Version: identity.Version}); err != nil {
		return Binding{}, err
	}
	existing, found, err := c.Repository.FindActiveExecutionBinding(ctx, session.ID)
	if err != nil {
		return Binding{}, err
	}
	generation := "gen_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("initial-execution:"+session.ID)).String()
	var predecessor Binding
	if found {
		if adopting, err := c.Repository.IsExecutionAdopting(ctx, existing.GenerationID); err != nil {
			return Binding{}, err
		} else if adopting {
			return Binding{}, fmt.Errorf("existing execution requires attachment reconciliation")
		}
		if existing.StoreID != identity.StoreID {
			return Binding{}, ErrBackendIdentityChanged
		}
		if worker.connection != nil && worker.binding.GenerationID == existing.GenerationID && existing.State == "accepted" && existing.Owner == owner {
			select {
			case <-worker.connection.Done():
				return Binding{}, fmt.Errorf("agent stream ended; execution reconciliation required")
			default:
			}
			if err := c.Repository.AssertExecutionAccepted(ctx, existing.GenerationID, owner, time.Now().UTC()); err != nil {
				return Binding{}, err
			}
			return existing, nil
		}
		if existing.State != "starting" || existing.Owner != owner {
			return Binding{}, fmt.Errorf("existing execution requires explicit reconciliation before use")
		}
		generation = existing.GenerationID
	} else {
		previous, found, err := c.Repository.FindLastExecutionBinding(ctx, session.ID)
		if err != nil {
			return Binding{}, err
		}
		if found {
			if previous.State != "retired" || previous.StoreID != identity.StoreID {
				return Binding{}, ErrOperationConflict
			}
			predecessor = previous
			generation = "gen_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("resume-execution:"+previous.GenerationID)).String()
		}
	}
	plan, err := c.Plans.Plan(ctx, session, generation)
	if err != nil {
		return Binding{}, err
	}
	environment, err := c.provisionEnvironment(ctx, session, identity, plan, owner, c.MaxActive)
	if err != nil {
		return Binding{}, err
	}
	if _, starting, err := c.Repository.FindEnvironmentOperation(ctx, "start:"+generation); err != nil {
		return Binding{}, err
	} else if !starting {
		if predecessor.GenerationID != "" {
			if _, err := ResumeRetainedExecution(ctx, c.Repository, c.Backend, predecessor.GenerationID, owner); err != nil {
				return Binding{}, err
			}
		} else if environment.ObservedState == "paused" {
			op, err := c.Repository.BeginInitialExecutionResume(ctx, environment.ID, owner, c.MaxActive)
			if err != nil {
				return Binding{}, err
			}
			if _, err := (EnvironmentService{Repository: c.Repository, Backend: c.Backend}).Run(ctx, OperationIntent{ID: op.ID, EnvironmentID: op.EnvironmentID, Kind: op.Kind, IdempotencyKey: op.IdempotencyKey, ExpectedFence: op.Fence - 1, Request: op.Request}); err != nil {
				return Binding{}, err
			}
		}
	}
	environment, err = c.Repository.GetEnvironmentBinding(ctx, environment.ID)
	if err != nil {
		return Binding{}, err
	}
	observed, err := c.Backend.Inspect(ctx, sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID})
	if err != nil {
		return Binding{}, err
	}
	if observed.Epoch != environment.Epoch || observed.State != "running" || observed.PlacementState != "active" || observed.MutationRevision != environment.MutationRevision {
		return Binding{}, fmt.Errorf("retained environment requires reconciled resume before a new execution")
	}
	request := sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: identity.StoreID, SandboxID: observed.SandboxID, Epoch: observed.Epoch, Handle: "bridge_" + generation}, IdempotencyKey: "bridge:" + generation, Command: "/usr/local/bin/harness-khora-launcher", Arguments: []string{"--session-id", session.ID, "--generation-id", generation, "--driver-id", session.Driver}, Environment: plan.Environment, Directory: "/workspace"}
	start := WorkerRequest{Reserve: ReserveBindingParams{GenerationID: generation, SessionID: session.ID, EnvironmentID: environment.ID, Owner: owner, Driver: session.Driver, SpecDigest: plan.SpecDigest, ManifestDigest: plan.ManifestDigest, LeaseTTL: c.LeaseTTL}, Process: request}
	var binding Binding
	var connection AgentConnection
	var retryUntil time.Time
	for {
		binding, connection, err = (Worker{Repository: c.Repository, Agents: c.Agents}).Start(lifetime, start)
		if !errors.Is(err, ErrReadinessInvalidated) {
			break
		}
		// Engine events may arrive between the captured watermark and readiness.
		// Retry only that fence with the original idempotent start operation;
		// generic startup failures still require explicit reconciliation.
		if retryUntil.IsZero() {
			retryUntil = time.Now().Add(5 * time.Second)
		}
		if !time.Now().Before(retryUntil) {
			return binding, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return binding, ctx.Err()
		case <-lifetime.Done():
			timer.Stop()
			return binding, lifetime.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return binding, err
	}
	worker.binding = binding
	worker.connection = connection
	return binding, nil
}

func (c *Controller) ReleaseIdle(ctx context.Context, session, generation, owner string) error {
	return c.releaseIdle(ctx, session, generation, owner, false)
}

func (c *Controller) MaintainCapture(ctx context.Context, session, owner string, maximum int, window time.Duration) (CaptureMaintenance, error) {
	if c.Repository == nil || c.Backend == nil || c.Lifetime == nil {
		return CaptureMaintenance{}, fmt.Errorf("capture maintenance controller ports required")
	}
	worker, lifetime, err := c.session(session)
	if err != nil {
		return CaptureMaintenance{}, err
	}
	requestContext, cancel := context.WithCancel(ctx)
	stopShutdown := context.AfterFunc(lifetime, cancel)
	defer func() { stopShutdown(); cancel() }()
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return (CaptureMaintenanceService{Repository: c.Repository, Backend: c.Backend, MaxActive: maximum, Window: window}).Step(requestContext, session, owner)
}

func (c *Controller) RecoverInterrupted(ctx context.Context, session, generation, owner, reason string) error {
	if c.Lifetime == nil || c.Repository == nil || c.Backend == nil || c.Routes == nil || c.RecoveryReader == nil || c.RecoveryProjector == nil {
		return fmt.Errorf("interrupted recovery controller ports required")
	}
	worker, lifetime, err := c.session(session)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithCancel(ctx)
	stopShutdown := context.AfterFunc(lifetime, cancel)
	defer func() { stopShutdown(); cancel() }()
	worker.mu.Lock()
	defer worker.mu.Unlock()
	binding, err := c.Repository.GetExecutionBinding(requestContext, generation)
	if err != nil {
		return err
	}
	if binding.SessionID != session {
		return ErrOperationConflict
	}
	if worker.connection != nil && worker.binding.GenerationID != generation {
		return ErrStaleOperation
	}
	prepared, err := c.Repository.NextInterruptedRecovery(requestContext, generation, owner, reason)
	if err != nil {
		return err
	}
	if prepared.Stage == "complete" {
		return nil
	}
	// Detaching cancels local ingest; the durable workflow separately proves
	// writer termination through pause before advancing recovery.
	if worker.connection != nil {
		if err := detachAgent(requestContext, worker.connection); err != nil {
			return err
		}
		worker.connection = nil
	}
	return (InterruptedRecoveryService{Repository: c.Repository, Backend: c.Backend, Routes: c.Routes, Reader: c.RecoveryReader, Projector: c.RecoveryProjector, Native: c.NativeRestorer}).Run(requestContext, generation, owner, reason)
}

// RecoverIdle is an explicit cold-recovery policy for an abandoned idle agent.
// It does not adopt a live turn or infer that transport loss killed a process.
func (c *Controller) RecoverIdle(ctx context.Context, session Session, generation, owner string) (Binding, error) {
	if c.Repository == nil {
		return Binding{}, fmt.Errorf("execution controller repository required")
	}
	latest, found, err := c.Repository.FindLastExecutionBinding(ctx, session.ID)
	if err != nil {
		return Binding{}, err
	}
	successor := "gen_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("resume-execution:"+generation)).String()
	if found && ((latest.GenerationID == generation && latest.State == "retired") || (latest.GenerationID == successor && latest.Owner == owner)) {
		return c.Ensure(ctx, session, owner)
	}
	if err := c.releaseIdle(ctx, session.ID, generation, owner, true); err != nil {
		return Binding{}, err
	}
	return c.Ensure(ctx, session, owner)
}

func (c *Controller) releaseIdle(ctx context.Context, session, generation, owner string, recovery bool) error {
	if c.Lifetime == nil || c.Repository == nil || c.Backend == nil || c.Routes == nil {
		return fmt.Errorf("idle release controller ports required")
	}
	worker, lifetime, err := c.session(session)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithCancel(ctx)
	stopShutdown := context.AfterFunc(lifetime, cancel)
	defer func() { stopShutdown(); cancel() }()
	worker.mu.Lock()
	defer worker.mu.Unlock()
	latest, found, err := c.Repository.FindLastExecutionBinding(requestContext, session)
	if err != nil {
		return err
	}
	if !found || latest.GenerationID != generation {
		return ErrStaleOperation
	}
	service := IdleReleaseService{Repository: c.Repository, Backend: c.Backend, Routes: c.Routes}
	run := service.Run
	if recovery {
		run = service.Recover
	}
	if err := run(requestContext, generation, owner); err != nil {
		return err
	}
	if worker.connection != nil {
		_ = worker.connection.Close()
		worker.connection = nil
	}
	return nil
}
func (c *Controller) session(id string) (*sessionWorker, context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, fmt.Errorf("execution controller is closed")
	}
	if c.runContext == nil {
		c.runContext, c.cancel = context.WithCancel(c.Lifetime)
	}
	if c.sessions == nil {
		c.sessions = map[string]*sessionWorker{}
	}
	if c.sessions[id] == nil {
		c.sessions[id] = &sessionWorker{}
	}
	return c.sessions[id], c.runContext, nil
}
func (c *Controller) Close() error {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	workers := make([]*sessionWorker, 0, len(c.sessions))
	for _, worker := range c.sessions {
		workers = append(workers, worker)
	}
	c.mu.Unlock()
	for _, worker := range workers {
		worker.mu.Lock()
		if worker.connection != nil {
			_ = worker.connection.Close()
			worker.connection = nil
		}
		worker.mu.Unlock()
	}
	return nil
}

// provisionEnvironment reconciles a session's retained environment against one
// generation's plan. Identity must match exactly; configuration is applied.
//
// A fresh environment is created with the generation's configuration directly. A
// retained one already holds its storage, so a changed configuration is applied
// through a fenced configure operation while it is paused, which is the only
// state the engine accepts an egress edit in. The applied declaration is recorded
// from the operation's own immutable request, never from the plan, so a lost
// reply cannot make the product believe an unapplied configuration is live.
func (c *Controller) provisionEnvironment(ctx context.Context, session Session, identity sandbox.Identity, plan LaunchPlan, owner string, maximum int) (Environment, error) {
	if plan.TemplateID == "" || plan.SpecDigest == "" || plan.ManifestDigest == "" || plan.CPUMillis <= 0 || plan.MemoryBytes <= 0 || plan.DiskBytes <= 0 || plan.WorkspaceBytes <= 0 || plan.JournalBytes <= 0 {
		return Environment{}, fmt.Errorf("driver plan requires pinned release and positive resource caps")
	}
	environment, found, err := c.Repository.FindSessionEnvironment(ctx, session.ID)
	if err != nil {
		return Environment{}, err
	}
	desiredSpec := desiredEnvironment{Driver: session.Driver, Template: plan.TemplateID, ManifestDigest: plan.ManifestDigest, CPU: plan.CPUMillis, Memory: plan.MemoryBytes, Disk: plan.DiskBytes, Workspace: plan.WorkspaceBytes, Journal: plan.JournalBytes}
	desired, err := json.Marshal(desiredSpec)
	if err != nil {
		return Environment{}, err
	}
	if !found {
		environment = Environment{ID: "env_" + session.ID, SessionID: session.ID, BackendName: c.BackendName, StoreID: identity.StoreID, Desired: desired}
		if err := c.Repository.CreateEnvironmentBinding(ctx, environment); err != nil {
			return Environment{}, err
		}
	} else {
		var persisted desiredEnvironment
		if err := json.Unmarshal(environment.Desired, &persisted); err != nil || environment.StoreID != identity.StoreID || environment.BackendName != c.BackendName || persisted != desiredSpec {
			return Environment{}, ErrOperationConflict
		}
	}
	storage := StorageService{Repository: c.Repository, Backend: c.Backend}
	mounts := append([]sandbox.Mount(nil), plan.Mounts...)
	for _, volume := range []struct {
		purpose, destination string
		capacity             int64
	}{{"workspace", "/workspace", plan.WorkspaceBytes}, {"bridge", "/harness-bridge", plan.JournalBytes}} {
		binding, err := storage.EnsureVolume(ctx, environment.ID, volume.purpose, volume.capacity)
		if err != nil {
			return Environment{}, err
		}
		mounts = append(mounts, sandbox.Mount{VolumeID: binding.VolumeID, Destination: volume.destination, Access: "exclusive"})
	}
	environment, err = c.Repository.GetEnvironmentBinding(ctx, environment.ID)
	if err != nil {
		return Environment{}, err
	}
	configuration := GenerationConfiguration{SpecDigest: plan.SpecDigest, Network: plan.Network, Mounts: mounts, Environment: plan.Environment}
	lifecycle := EnvironmentService{Repository: c.Repository, Backend: c.Backend}
	if environment.SandboxID == "" {
		request := sandbox.CreateRequest{IdempotencyKey: "create:" + environment.ID, TemplateID: plan.TemplateID, CPUMillis: plan.CPUMillis, MemoryBytes: plan.MemoryBytes, DiskBytes: plan.DiskBytes, Mounts: mounts, Network: plan.Network}
		raw, err := json.Marshal(EnvironmentRequest{Reference: sandbox.Reference{StoreID: identity.StoreID}, Create: &request, Configuration: &configuration})
		if err != nil {
			return Environment{}, err
		}
		intent := OperationIntent{ID: "create:" + environment.ID, EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: request.IdempotencyKey, ExpectedFence: environment.Fence, Request: raw}
		if previous, found, err := c.Repository.FindEnvironmentOperation(ctx, intent.ID); err != nil {
			return Environment{}, err
		} else if found {
			intent.ExpectedFence = previous.Fence - 1
		}
		if _, err := c.Repository.BeginSessionProvisioning(ctx, intent, owner, maximum); err != nil {
			return Environment{}, err
		}
		if _, err := lifecycle.Run(ctx, intent); err != nil {
			return Environment{}, err
		}
		return c.Repository.GetEnvironmentBinding(ctx, environment.ID)
	}
	if err := c.applyGenerationConfiguration(ctx, environment, configuration, owner); err != nil {
		return Environment{}, err
	}
	return c.Repository.GetEnvironmentBinding(ctx, environment.ID)
}

// applyGenerationConfiguration brings a retained environment to one generation's
// declaration. An already-applied declaration is a no-op, so an unchanged plan
// costs no engine mutation and a resumed session is not forced through a pause.
func (c *Controller) applyGenerationConfiguration(ctx context.Context, environment Environment, configuration GenerationConfiguration, owner string) error {
	if environment.SandboxID == "" {
		return ErrOperationConflict
	}
	current, applied, err := environment.AppliedConfiguration()
	if err != nil {
		return err
	}
	if applied && current.SpecDigest == configuration.SpecDigest && sandbox.SameConfiguration(sandbox.Configuration{Network: current.Network, Mounts: current.Mounts}, configuration.Network, configuration.Mounts) && equalStrings(current.Environment, configuration.Environment) {
		return nil
	}
	// The engine refuses an egress edit on a running sandbox, so a retained
	// environment must be observed paused before its declaration changes. This is
	// a pending fence for the caller to retry after idle release, not a failure.
	if environment.ObservedState != "paused" {
		return ErrBusy
	}
	operation, err := c.Repository.BeginGenerationConfiguration(ctx, environment.ID, owner, configuration)
	if err != nil {
		return err
	}
	_, err = (EnvironmentService{Repository: c.Repository, Backend: c.Backend}).Run(ctx, OperationIntent{ID: operation.ID, EnvironmentID: operation.EnvironmentID, Kind: operation.Kind, IdempotencyKey: operation.IdempotencyKey, ExpectedFence: operation.Fence - 1, Request: operation.Request})
	return err
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *Controller) InspectEnvironment(ctx context.Context, id, owner string) (sandbox.Observation, error) {
	return InspectEnvironment(ctx, c.Repository, c.Backend, id, owner)
}
