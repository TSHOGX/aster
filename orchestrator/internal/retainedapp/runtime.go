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
	"harness-platform/orchestrator/internal/driverstate"
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
}
type Options struct {
	Backend                                                                                Backend
	Plans                                                                                  *launchplan.Source
	Routes                                                                                 execution.RouteRevoker
	Grants                                                                                 execution.GrantRevoker
	BackendName, OwnerUUID, NewSessionBackend, PreparedRoot                                string
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

// Attach validates all ports before changing the server. Existing session
// routing remains persisted; only future creations use NewSessionBackend.
func Attach(ctx context.Context, app *server.Server, st *store.Store, p Options) (*Runtime, error) {
	if app == nil || st == nil || p.Backend == nil || p.Plans == nil || p.Routes == nil || p.Grants == nil || p.BackendName == "" || p.OwnerUUID == "" || p.MaxActive <= 0 || p.InventoryLimit <= 0 || p.LeaseTTL <= 0 || p.PollInterval <= 0 || p.EventInterval <= 0 || p.ActionTimeout <= 0 || p.CaptureWindow <= 0 || p.InventoryInterval <= 0 || (p.NewSessionBackend != "khora" && p.NewSessionBackend != "legacy") {
		return nil, fmt.Errorf("retained application requires explicit release, gateway, owner and bounded service configuration")
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
	content, err := prepared.New(p.PreparedRoot)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	hooks := app.ExecutionHooks()
	launcher := agentbridge.Launcher{Backend: p.Backend, Store: store.BoundBridgeStore{Store: st}, Receipts: st, Commands: st, IdleReplay: st, ActiveReplay: st, Requests: st, LeaseTTL: p.LeaseTTL, AfterCommit: hooks.AfterCommit, BeforeComplete: hooks.BeforeComplete, AfterEventCommit: hooks.AfterEventCommit}
	controller := &execution.Controller{Repository: st, Backend: p.Backend, Agents: launcher, Plans: p.Plans, BackendName: p.BackendName, LeaseTTL: p.LeaseTTL, MaxActive: p.MaxActive, Lifetime: lifetime, Routes: p.Routes, GrantRevoker: p.Grants, RecoveryReader: agentbridge.RecoveryReader{Backend: p.Backend, Records: st, AfterEventCommit: hooks.AfterEventCommit}, RecoveryProjector: app, NativeRestorer: driverstate.Restorer{Backend: p.Backend}}
	reader := sessionfiles.Reader{Repository: st, Backend: p.Backend}
	writer := &sessionfiles.Writer{Repository: st, Backend: p.Backend, Lifecycle: p.Backend, Content: content, MaxActive: p.MaxActive, Provisioner: controller}
	inventory := sessionfiles.Scanner{Repository: st, Source: reader, Owner: owner, Limit: p.InventoryLimit, AfterCommit: hooks.AfterEventCommit}
	reconciler := &execution.Reconciler{Repository: st, Controller: controller, Workspace: writer, Owner: owner, MaxActive: p.MaxActive, CaptureWindow: p.CaptureWindow, Interval: p.PollInterval, ActionTimeout: p.ActionTimeout, AfterWorkspace: inventory.ScanSession, AfterRetirement: hooks.AfterEventCommit, OnError: func(kind execution.ReconcileKind, id string, err error) {
		if p.OnError != nil {
			p.OnError(string(kind)+":"+id, err)
		}
	}}
	if err := app.SetExecutionController(controller, p.NewSessionBackend); err != nil {
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
	return &Runtime{Prepared: &prepared.Collector{Store: content, Repository: st, Owner: owner}, EngineEvents: &execution.EventReconciler{StoreID: identity.StoreID, Owner: owner, Source: p.Backend, Repository: st}, Controller: controller, Writer: writer, Inventory: inventory, Reconciler: reconciler, ctx: lifetime, cancel: cancel, options: p}, nil
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
	results := make(chan error, 4)
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
	second, third, fourth := <-results, <-results, <-results
	return errors.Join(err, second, third, fourth)
}
func (r *Runtime) Close() error { r.cancel(); return r.Controller.Close() }
