// Package retainedapp composes retained execution services for an application.
// Gateways and release plans are explicit inputs, never inferred from guest data.
package retainedapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/agentbridge"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/driverstate"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/launchplan"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/sessionfiles"
	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	"harness-platform/orchestrator/internal/workspace/guest"
	"harness-platform/orchestrator/internal/workspace/prepared"
)

type Backend interface {
	sandbox.EventSource
	execution.ControllerBackend
	sessionfiles.Backend
	content.Backend
}
type GatewayMaintenance interface {
	Run(context.Context, time.Duration, time.Duration, func(string, error)) error
}

type Options struct {
	GatewayMaintenance GatewayMaintenance
	Backend            Backend
	Plans              *launchplan.Source
	// Content is every authored revision a launch may mount. It is published and
	// verified before the server accepts work, so no session can reach a plan whose
	// content this engine store has not established.
	Content      []AuthoredContent
	ContentBytes int64
	// Endpoints is the declared inventory this deployment composes per generation.
	// An empty catalog means no session receives external service access.
	Connectivity                                                                           egress.Catalog
	Routes                                                                                 execution.RouteRevoker
	Activator                                                                              execution.RouteActivator
	Grants                                                                                 execution.GrantRevoker
	BackendName, OwnerUUID, PreparedRoot                                                   string
	MaxActive, InventoryLimit                                                              int
	LeaseTTL, PollInterval, EventInterval, ActionTimeout, CaptureWindow, InventoryInterval time.Duration
	OnError                                                                                func(string, error)
}
type Runtime struct {
	Controller   *execution.Controller
	Writer       *sessionfiles.Writer
	Inventory    sessionfiles.Scanner
	Reconciler   *execution.Reconciler
	EngineEvents *execution.EventReconciler
	Prepared     *prepared.Collector
	ctx          context.Context
	cancel       context.CancelFunc
	options      Options
	mu           sync.Mutex
	running      bool
}

// Attach validates all Khora application ports before changing the server.
func Attach(ctx context.Context, app *server.Server, st *store.Store, p Options) (*Runtime, error) {
	if app == nil || st == nil || p.Backend == nil || p.Plans == nil || p.Routes == nil || p.Grants == nil || p.BackendName == "" || p.OwnerUUID == "" || p.MaxActive <= 0 || p.InventoryLimit <= 0 || p.LeaseTTL <= 0 || p.PollInterval <= 0 || p.EventInterval <= 0 || p.ActionTimeout <= 0 || p.CaptureWindow <= 0 || p.InventoryInterval <= 0 {
		return nil, fmt.Errorf("retained application requires explicit release, gateway, owner and bounded service configuration")
	}
	if p.ContentBytes <= 0 {
		return nil, fmt.Errorf("positive content volume capacity required")
	}
	drivers := p.Plans.Drivers()
	if len(drivers) == 0 || p.InventoryLimit > 10000 {
		return nil, fmt.Errorf("enabled release drivers and bounded inventory required")
	}
	owner := store.GenerationLeaseOwner(p.OwnerUUID)
	if err := st.AssertExecutionOwner(ctx, owner); err != nil {
		return nil, err
	}
	identity, err := p.Backend.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if err := st.PinBackendIdentity(ctx, execution.BackendIdentity{Name: p.BackendName, StoreID: identity.StoreID, Version: identity.Version}); err != nil {
		return nil, err
	}
	// Publication runs before any product port is installed. A revision that
	// cannot be published and verified must stop startup rather than leave a
	// daemon that accepts sessions and then refuses every launch.
	publisher := content.Publisher{Repository: st, Backend: p.Backend}
	for _, authored := range p.Content {
		if _, err := publisher.Publish(ctx, authored.Purpose, authored.Revision, p.ContentBytes); err != nil {
			return nil, fmt.Errorf("publish authored %s content %s: %w", authored.Purpose, authored.Revision.Digest, err)
		}
	}
	plans, err := p.Plans.WithPublishedContent(identity.StoreID, st)
	if err != nil {
		return nil, err
	}
	uploads, err := prepared.New(p.PreparedRoot)
	if err != nil {
		return nil, err
	}
	var composer execution.NetworkComposer
	if len(p.Connectivity.Policies) != 0 {
		if len(p.Connectivity.Endpoints) != 0 && p.Activator == nil {
			return nil, fmt.Errorf("declared services require a gateway route activator")
		}
		composer = store.NetworkComposer{Catalog: p.Connectivity, Allocator: st}
	}
	lifetime, cancel := context.WithCancel(ctx)
	hooks := app.ExecutionHooks()
	beforeComplete := func(ctx context.Context, completion *store.CompleteTurnParams) error {
		if err := hooks.BeforeComplete(ctx, completion); err != nil {
			return err
		}
		grants, err := st.PrepareTurnEgressDrain(ctx, *completion)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			if p.Grants == nil {
				return fmt.Errorf("turn completion requires gateway flow drain")
			}
			if err := p.Grants.RevokeGrant(ctx, grant); err != nil {
				return err
			}
			if err := st.ConfirmGatewayGrantDrained(ctx, grant); err != nil {
				return err
			}
		}
		return nil
	}
	launcher := agentbridge.Launcher{Backend: p.Backend, Store: st, Receipts: st, Commands: st, IdleReplay: st, ActiveReplay: st, Requests: st, LeaseTTL: p.LeaseTTL, AfterCommit: hooks.AfterCommit, BeforeComplete: beforeComplete, AfterEventCommit: hooks.AfterEventCommit}
	controller := &execution.Controller{Repository: st, Backend: p.Backend, Agents: launcher, Plans: plans, BackendName: p.BackendName, LeaseTTL: p.LeaseTTL, MaxActive: p.MaxActive, Lifetime: lifetime, Routes: p.Routes, Activator: p.Activator, Connectivity: composer, GrantRevoker: p.Grants, RecoveryReader: agentbridge.RecoveryReader{Backend: p.Backend, Records: st, AfterEventCommit: hooks.AfterEventCommit}, RecoveryProjector: app, NativeRestorer: driverstate.Restorer{Backend: p.Backend}}
	reader := sessionfiles.Reader{Repository: st, Backend: p.Backend}
	writer := &sessionfiles.Writer{Repository: st, Backend: p.Backend, Lifecycle: p.Backend, Content: uploads, MaxActive: p.MaxActive, Provisioner: controller}
	inventory := sessionfiles.Scanner{Repository: st, Source: reader, Owner: owner, Limit: p.InventoryLimit, AfterCommit: hooks.AfterEventCommit}
	reconciler := &execution.Reconciler{Repository: st, Controller: controller, Workspace: writer, Owner: owner, MaxActive: p.MaxActive, CaptureWindow: p.CaptureWindow, Interval: p.PollInterval, ActionTimeout: p.ActionTimeout, AfterWorkspace: inventory.ScanSession, AfterRetirement: hooks.AfterEventCommit, OnError: func(kind execution.ReconcileKind, id string, err error) {
		if p.OnError != nil {
			p.OnError(string(kind)+":"+id, err)
		}
	}}
	if err := app.SetExecutionController(controller); err != nil {
		cancel()
		return nil, err
	}
	app.SetOwnerUUID(p.OwnerUUID)
	if err := app.SetRetainedDrivers(drivers); err != nil {
		cancel()
		return nil, err
	}
	app.SetWorkspaceReader(reader)
	app.SetWorkspaceWriter(writer)
	app.SetWorkspaceInventory(inventory)
	app.SetNativeStateValidator(func(ctx context.Context, input store.DriverStateValidationInput, turn int64) (workspace.File, error) {
		return driverstate.ValidateRemote(ctx, input.CanonicalPayload, fmt.Sprint(turn), fmt.Sprintf("native-state-%s-%d", input.Evidence.GenerationID, turn), guest.Files{Backend: p.Backend, Reference: input.Reference, Root: "/root/.pi/agent/sessions"})
	})
	return &Runtime{Prepared: &prepared.Collector{Store: uploads, Repository: st, Owner: owner}, EngineEvents: &execution.EventReconciler{StoreID: identity.StoreID, Owner: owner, Source: p.Backend, Repository: st}, Controller: controller, Writer: writer, Inventory: inventory, Reconciler: reconciler, ctx: lifetime, cancel: cancel, options: p}, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return fmt.Errorf("retained application already running")
	}
	r.running = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.running = false; r.mu.Unlock() }()
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	results := make(chan error, 5)
	workers := 4
	if r.options.GatewayMaintenance != nil {
		workers++
		go func() {
			results <- r.options.GatewayMaintenance.Run(r.ctx, r.options.EventInterval, r.options.ActionTimeout, func(id string, err error) {
				if r.options.OnError != nil {
					r.options.OnError("gateway:"+id, err)
				}
			})
		}()
	}
	go func() { results <- r.Reconciler.Run(r.ctx) }()
	go func() {
		results <- r.Inventory.Run(r.ctx, r.options.InventoryInterval, r.options.ActionTimeout, func(session string, err error) {
			if r.options.OnError != nil {
				r.options.OnError("inventory:"+session, err)
			}
		})
	}()
	go func() {
		results <- r.EngineEvents.Run(r.ctx, r.options.EventInterval, r.options.ActionTimeout, func(err error) {
			if r.options.OnError != nil {
				r.options.OnError("engine-events", err)
			}
		})
	}()
	go func() {
		results <- r.Prepared.Run(r.ctx, r.options.InventoryInterval, r.options.ActionTimeout, func(err error) {
			if r.options.OnError != nil {
				r.options.OnError("prepared-content", err)
			}
		})
	}()
	err := <-results
	r.cancel()
	for range workers - 1 {
		err = errors.Join(err, <-results)
	}
	return err
}
func (r *Runtime) Close() error { r.cancel(); return r.Controller.Close() }
