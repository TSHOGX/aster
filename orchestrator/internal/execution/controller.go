package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
)

type ControllerRepository interface {
	AssertExecutionAccepted(context.Context, string, string, time.Time) error
	EnvironmentRepository
	StorageRepository
	WorkerRepository
	PinBackendIdentity(context.Context, BackendIdentity) error
	CreateEnvironmentBinding(context.Context, Environment) error
	FindSessionEnvironment(context.Context, string) (Environment, bool, error)
	FindActiveExecutionBinding(context.Context, string) (Binding, bool, error)
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

type desiredEnvironment struct {
	Driver, Template  string
	CPU, Memory, Disk int64
}

type Controller struct {
	Repository  ControllerRepository
	Backend     ControllerBackend
	Agents      AgentLauncher
	Plans       PlanSource
	BackendName string
	LeaseTTL    time.Duration
	Lifetime    context.Context
	mu          sync.Mutex
	closed      bool
	runContext  context.Context
	cancel      context.CancelFunc
	sessions    map[string]*sessionWorker
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
	if found {
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
	}
	plan, err := c.Plans.Plan(ctx, session, generation)
	if err != nil {
		return Binding{}, err
	}
	if plan.TemplateID == "" || plan.SpecDigest == "" || plan.ManifestDigest == "" || plan.CPUMillis <= 0 || plan.MemoryBytes <= 0 || plan.DiskBytes <= 0 || plan.WorkspaceBytes <= 0 || plan.JournalBytes <= 0 {
		return Binding{}, fmt.Errorf("driver plan requires pinned release and positive resource caps")
	}
	environment, found, err := c.Repository.FindSessionEnvironment(ctx, session.ID)
	if err != nil {
		return Binding{}, err
	}
	desiredSpec := desiredEnvironment{session.Driver, plan.TemplateID, plan.CPUMillis, plan.MemoryBytes, plan.DiskBytes}
	desired, _ := json.Marshal(desiredSpec)
	if !found {
		environment = Environment{ID: "env_" + session.ID, SessionID: session.ID, BackendName: c.BackendName, StoreID: identity.StoreID, Desired: desired}
		if err := c.Repository.CreateEnvironmentBinding(ctx, environment); err != nil {
			return Binding{}, err
		}
	} else {
		var persisted desiredEnvironment
		if err := json.Unmarshal(environment.Desired, &persisted); err != nil || environment.StoreID != identity.StoreID || environment.BackendName != c.BackendName || persisted != desiredSpec {
			return Binding{}, ErrOperationConflict
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
			return Binding{}, err
		}
		mounts = append(mounts, sandbox.Mount{VolumeID: binding.VolumeID, Destination: volume.destination, Access: "exclusive"})
	}
	environment, err = c.Repository.GetEnvironmentBinding(ctx, environment.ID)
	if err != nil {
		return Binding{}, err
	}
	lifecycle := EnvironmentService{Repository: c.Repository, Backend: c.Backend}
	var observed sandbox.Observation
	if environment.SandboxID == "" {
		request := sandbox.CreateRequest{IdempotencyKey: "create:" + environment.ID, TemplateID: plan.TemplateID, CPUMillis: plan.CPUMillis, MemoryBytes: plan.MemoryBytes, DiskBytes: plan.DiskBytes, Mounts: mounts, Network: plan.Network}
		raw, _ := json.Marshal(EnvironmentRequest{Reference: sandbox.Reference{StoreID: identity.StoreID}, Create: &request})
		intent := OperationIntent{ID: "create:" + environment.ID, EnvironmentID: environment.ID, Kind: "create", IdempotencyKey: request.IdempotencyKey, ExpectedFence: environment.Fence, Request: raw}
		if previous, found, err := c.Repository.FindEnvironmentOperation(ctx, intent.ID); err != nil {
			return Binding{}, err
		} else if found {
			intent.ExpectedFence = previous.Fence - 1
		}
		observed, err = lifecycle.Run(ctx, intent)
		if err != nil {
			return Binding{}, err
		}
	} else {
		observed, err = c.Backend.Inspect(ctx, sandbox.Reference{StoreID: environment.StoreID, SandboxID: environment.SandboxID})
		if err != nil {
			return Binding{}, err
		}
		if observed.Epoch != environment.Epoch || observed.State != "running" || observed.PlacementState != "active" || observed.MutationRevision != environment.MutationRevision {
			return Binding{}, fmt.Errorf("retained environment requires reconciled resume before a new execution")
		}
		// The initial generation identity is stable across interrupted starts;
		// ReserveExecutionBinding refuses a previously retired incarnation.

	}
	request := sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: identity.StoreID, SandboxID: observed.SandboxID, Epoch: observed.Epoch, Handle: "bridge_" + generation}, IdempotencyKey: "bridge:" + generation, Command: "/usr/local/bin/harness-khora-launcher", Arguments: []string{"--session-id", session.ID, "--generation-id", generation, "--driver-id", session.Driver}, Environment: plan.Environment, Directory: "/workspace"}
	binding, connection, err := (Worker{Repository: c.Repository, Agents: c.Agents}).Start(lifetime, WorkerRequest{Reserve: ReserveBindingParams{GenerationID: generation, SessionID: session.ID, EnvironmentID: environment.ID, Owner: owner, Driver: session.Driver, SpecDigest: plan.SpecDigest, ManifestDigest: plan.ManifestDigest, LeaseTTL: c.LeaseTTL}, Process: request})
	if err != nil {
		return binding, err
	}
	worker.binding = binding
	worker.connection = connection
	return binding, nil
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
