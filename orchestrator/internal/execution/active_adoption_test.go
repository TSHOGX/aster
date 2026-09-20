package execution_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/store"
)

type activeDrainRepository struct{ *store.Store }

func (r activeDrainRepository) BeginActiveExecutionAdoption(ctx context.Context, generation, owner string, ttl time.Duration) (execution.ExecutionAdoption, error) {
	a, err := r.Store.BeginActiveExecutionAdoption(ctx, generation, owner, ttl)
	a.Grants = append(a.Grants, "old-established-flow")
	return a, err
}

type activeDrain struct {
	fail  bool
	calls int
}

func (d *activeDrain) RevokeGrant(context.Context, string) error {
	d.calls++
	if d.fail {
		return errors.New("flow drain unavailable")
	}
	return nil
}

type activeAttacher struct {
	*scriptedLauncher
	store *store.Store
	calls int
}

func (a *activeAttacher) AttachActive(ctx context.Context, b execution.Binding, ref sandbox.ProcessReference) (execution.AgentConnection, error) {
	a.calls++
	if ref.Handle != b.ProcessHandle || ref.Epoch != b.Epoch {
		return nil, errors.New("active attachment changed process")
	}
	if err := a.store.AssertExecutionAccepted(ctx, b.GenerationID, b.Owner, time.Now()); err == nil {
		return nil, errors.New("attachment restored authority before readiness")
	}
	connection := &readyAgent{ready: make(chan error, 1)}
	connection.ready <- nil
	return connection, nil
}

func TestActiveControllerDrainsBeforeAttachmentAndRetriesSameAttempt(t *testing.T) {
	ctx := context.Background()
	original, st, backend, launcher, owner := controllerFixture(t)
	b, err := original.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.EnqueueTurn(ctx, b.SessionID, "original prompt", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := (store.BoundBridgeStore{Store: st}).ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatalf("initial claim: %t %v", ok, err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	drain := &activeDrain{fail: true}
	attacher := &activeAttacher{scriptedLauncher: launcher, store: st}
	controller := &execution.Controller{Repository: activeDrainRepository{st}, Backend: backend, Agents: attacher, GrantRevoker: drain, Plans: staticPlan{}, BackendName: "engine", Lifetime: ctx, LeaseTTL: time.Minute}
	defer controller.Close()
	if _, err := controller.ReconnectActive(ctx, b.SessionID, b.GenerationID, owner); err == nil {
		t.Fatal("unresolved drain allowed attachment")
	}
	if attacher.calls != 0 {
		t.Fatal("attachment preceded flow drain")
	}
	if _, err := controller.Ensure(ctx, execution.Session{ID: b.SessionID, Driver: "sh"}, owner); err == nil {
		t.Fatal("failed adoption fell through process startup")
	}
	drain.fail = false
	adopted, err := controller.ReconnectActive(ctx, b.SessionID, b.GenerationID, owner)
	if err != nil || adopted.GenerationID != b.GenerationID || adopted.ProcessHandle != b.ProcessHandle || adopted.Epoch != b.Epoch || drain.calls != 2 || attacher.calls != 1 || len(launcher.requests) != 1 {
		t.Fatalf("active retry changed execution or skipped drain: %+v %v", adopted, err)
	}
	var attempt int
	if err := st.DBForTest().QueryRow(`SELECT attempt FROM turns WHERE id=?`, turn).Scan(&attempt); err != nil || attempt != 1 {
		t.Fatalf("active retry created another turn attempt: %d %v", attempt, err)
	}
}

func TestActiveControllerWaitsForRemoteGatewayDrain(t *testing.T) {
	ctx := context.Background()
	original, st, backend, launcher, owner := controllerFixture(t)
	b, err := original.Ensure(ctx, execution.Session{ID: "session", Driver: "sh"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueTurn(ctx, b.SessionID, "original prompt", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := (store.BoundBridgeStore{Store: st}).ClaimNextTurn(ctx, store.ClaimNextTurnParams{SessionID: b.SessionID, GenerationID: b.GenerationID, Owner: owner, RequestID: "claim", LeaseTTL: time.Minute}); err != nil || !ok {
		t.Fatalf("initial claim: %t %v", ok, err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	flows := gateway.NewFlows()
	if err := flows.Activate("old-route", 1); err != nil {
		t.Fatal(err)
	}
	upstream, flow, err := flows.Begin(ctx, egress.Authorization{RequestContext: egress.RequestContext{BindingID: "old-route", BindingRevision: 1, GrantID: "old-established-flow", RequestID: "original-request"}, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Finish()
	handler, err := gateway.ControlHandler(flows, gateway.ControlOptions{ID: "data", Protocol: egress.DorisMySQL, ApplicationUID: uint32(os.Getuid()), MaxOperationTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gateway.sock")
	listener, err := gateway.ListenControl(path, os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ConnContext: gateway.PeerContext}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-finished }()
	remote, err := gateway.NewRemotes(ctx, []gateway.RemoteConfig{{ID: "data", Protocol: egress.DorisMySQL, Socket: path, PeerUID: uint32(os.Getuid()), Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	attacher := &activeAttacher{scriptedLauncher: launcher, store: st}
	controller := &execution.Controller{Repository: activeDrainRepository{st}, Backend: backend, Agents: attacher, GrantRevoker: remote, Plans: staticPlan{}, BackendName: "engine", Lifetime: ctx, LeaseTTL: time.Minute}
	defer controller.Close()
	if _, err := controller.ReconnectActive(ctx, b.SessionID, b.GenerationID, owner); err == nil {
		t.Fatal("remote timeout allowed attachment before upstream finish")
	}
	if upstream.Err() == nil || attacher.calls != 0 {
		t.Fatal("remote drain failed to cancel or attached before termination")
	}
	flow.Finish()
	if _, err := controller.ReconnectActive(ctx, b.SessionID, b.GenerationID, owner); err != nil {
		t.Fatal(err)
	}
	if attacher.calls != 1 || len(launcher.requests) != 1 {
		t.Fatal("settled remote drain did not preserve original process")
	}
}
