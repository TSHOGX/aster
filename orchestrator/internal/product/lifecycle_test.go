package product

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/sandbox"
)

type fakeLifecycle struct {
	configurations map[string]sandbox.CreateRequest
	mu             sync.Mutex
	store          string
	volumes        map[string]sandbox.Volume
	volumeKeys     map[string]string
	creates        map[string]string
	sandboxes      map[string]sandbox.Observation
	mounts         map[string][]sandbox.Mount
	fail           map[string]int
	calls          map[string]int
	// projections and epochs record the host-owned guest files this Project wrote
	// and the placement epoch each was written under.
	projections map[string]map[string]string
	epochs      map[string]uint64
}

func fakeBackend() *fakeLifecycle {
	return &fakeLifecycle{configurations: map[string]sandbox.CreateRequest{}, store: "engine", volumes: map[string]sandbox.Volume{}, volumeKeys: map[string]string{}, creates: map[string]string{}, sandboxes: map[string]sandbox.Observation{}, mounts: map[string][]sandbox.Mount{}, fail: map[string]int{}, calls: map[string]int{}, projections: map[string]map[string]string{}, epochs: map[string]uint64{}}
}
func (f *fakeLifecycle) lost(kind string) error {
	f.calls[kind]++
	if f.fail[kind] > 0 {
		f.fail[kind]--
		return errors.New("lost " + kind + " reply")
	}
	return nil
}
func (f *fakeLifecycle) Identity(context.Context) (sandbox.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sandbox.Identity{StoreID: f.store, Version: "test", ManualColdRecovery: true}, nil
}
func (f *fakeLifecycle) CreateVolume(_ context.Context, key string, capacity int64) (sandbox.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.volumeKeys[key]
	if id == "" {
		id = fmt.Sprintf("v%d", len(f.volumeKeys)+1)
		f.volumeKeys[key] = id
		f.volumes[id] = sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: f.store, VolumeID: id}, Capacity: capacity}
	}
	v := f.volumes[id]
	return v, f.lost("volume")
}
func (f *fakeLifecycle) Create(_ context.Context, r sandbox.CreateRequest) (sandbox.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.creates[r.IdempotencyKey]
	if id == "" {
		id = fmt.Sprintf("box%d", len(f.creates)+1)
		f.creates[r.IdempotencyKey] = id
		f.sandboxes[id] = sandbox.Observation{Reference: sandbox.Reference{StoreID: f.store, SandboxID: id, MutationRevision: "r1", Epoch: 1}, State: "running", PlacementState: "active"}
		f.mounts[id] = r.Mounts
		f.configurations[id] = r
	}
	return f.sandboxes[id], f.lost("create")
}
func (f *fakeLifecycle) Inspect(_ context.Context, ref sandbox.Reference) (sandbox.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.sandboxes[ref.SandboxID]
	if !ok {
		return o, &sandbox.Error{Class: sandbox.NotFound}
	}
	return o, nil
}
func (f *fakeLifecycle) Pause(_ context.Context, ref sandbox.Reference) (sandbox.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.sandboxes[ref.SandboxID]
	if o.Reference != ref {
		return o, ErrConflict
	}
	o.State, o.PlacementState = "paused", "lost"
	o.MutationRevision += "p"
	f.sandboxes[ref.SandboxID] = o
	return o, f.lost("pause")
}
func (f *fakeLifecycle) Resume(_ context.Context, ref sandbox.Reference) (sandbox.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.sandboxes[ref.SandboxID]
	if o.Reference != ref {
		return o, ErrConflict
	}
	o.State, o.PlacementState = "running", "active"
	o.Epoch++
	o.MutationRevision += "r"
	f.sandboxes[ref.SandboxID] = o
	return o, f.lost("resume")
}
func (f *fakeLifecycle) Destroy(_ context.Context, ref sandbox.Reference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.sandboxes[ref.SandboxID]
	if ok && o.Reference != ref {
		return ErrConflict
	}
	delete(f.sandboxes, ref.SandboxID)
	return f.lost("destroy")
}
func (f *fakeLifecycle) DeleteVolume(_ context.Context, ref sandbox.VolumeReference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.volumes, ref.VolumeID)
	return f.lost("delete-volume")
}
func (f *fakeLifecycle) WriteProjection(_ context.Context, ref sandbox.Reference, path string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.sandboxes[ref.SandboxID]
	if !ok || o.Reference != ref || o.State != "running" {
		return ErrConflict
	}
	if f.projections[ref.SandboxID] == nil {
		f.projections[ref.SandboxID] = map[string]string{}
	}
	f.projections[ref.SandboxID][path] = string(content)
	f.epochs[ref.SandboxID+path] = ref.Epoch
	return f.lost("projection")
}
func testProfile() Profile {
	return Profile{StoreID: "engine", TemplateID: "test", CPUMillis: 1000, MemoryBytes: 256 << 20, DiskBytes: 128 << 20, WorkspaceBytes: 64 << 20, RuntimeBytes: 32 << 20}
}
func lifecycleFixture(t *testing.T) (*Lifecycle, Scope, *fakeLifecycle) {
	t.Helper()
	s, a, _ := fixture(t)
	f := fakeBackend()
	return &Lifecycle{Store: s, Backend: f, MaxProjects: 1}, a, f
}
func drive(t *testing.T, l *Lifecycle, project string) {
	t.Helper()
	for i := 0; i < 12; i++ {
		err := l.Reconcile(ctx, project)
		if errors.Is(err, ErrNotFound) {
			return
		}
		must(t, err)
	}
	t.Fatal("reconciliation did not settle")
}

// settle converges a Project whose backend is injecting lost replies, so a test
// can assert the eventual state without treating each injected loss as fatal.
func settle(t *testing.T, l *Lifecycle, project string) {
	t.Helper()
	for i := 0; i < 24; i++ {
		if errors.Is(l.Reconcile(ctx, project), ErrNotFound) {
			return
		}
	}
	t.Fatal("reconciliation did not settle after injected losses")
}

func TestConcurrentProjectFirstUseAndOriginalCreateKeys(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	f.fail["volume"] = 1
	f.fail["create"] = 1
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- l.EnsureActive(ctx, a, testProfile()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	if err := l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("expected lost volume reply")
	}
	r, err := l.Store.Resources(ctx, a)
	must(t, err)
	if !r.Occupied || r.WorkspaceVolume != "" {
		t.Fatal(r)
	}
	must(t, l.Reconcile(ctx, a.ProjectID))
	must(t, l.Reconcile(ctx, a.ProjectID))
	if err := l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("expected lost create reply")
	}
	drive(t, l, a.ProjectID)
	r, err = l.ActiveResources(ctx, a)
	must(t, err)
	if len(f.creates) != 1 || len(f.volumes) != 2 || !r.Occupied || r.Reference.Epoch != 1 {
		t.Fatal(r, f.creates, f.volumes)
	}
	mounts := f.mounts[r.Reference.SandboxID]
	if len(mounts) != 2 || mounts[0].Destination != "/workspace" || mounts[1].Destination != "/runtime" {
		t.Fatal(mounts)
	}
	for i := 0; i < 10; i++ {
		_, err = l.Store.CreateSession(ctx, a, fmt.Sprintf("s%d", i), "Agent")
		must(t, err)
	}
	must(t, l.EnsureActive(ctx, a, testProfile()))
	if len(f.creates) != 1 {
		t.Fatal("Session reserved placement")
	}
	wantError(t, l.EnsureActive(ctx, Scope{"bob", "b"}, testProfile()), ErrCapacity)
}

func TestIdleRevisionsQueuedSessionsHeldAndRetainedTerminal(t *testing.T) {
	l, a, _ := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	now := time.Now()
	l.Store.now = func() time.Time { return now }
	for _, id := range []string{"a1", "a2"} {
		_, err := l.Store.CreateSession(ctx, a, id, "Agent")
		must(t, err)
	}
	_, err := l.Store.AdmitTurn(ctx, a, "a1", "held", "held", true)
	must(t, err)
	timer, err := l.Store.ArmIdle(ctx, a, time.Second)
	must(t, err)
	now = now.Add(2 * time.Second)
	_, err = l.Store.AdmitTurn(ctx, a, "a1", "new", "one", false)
	must(t, err)
	_, err = l.Store.AdmitTurn(ctx, a, "a2", "new", "two", false)
	must(t, err)
	wantError(t, l.PauseIdle(ctx, a, timer), ErrConflict)
	bs, err := l.Store.Blockers(ctx, a)
	must(t, err)
	if len(bs) != 2 {
		t.Fatal(bs)
	}
	_, err = l.Store.ArmIdle(ctx, a, time.Second)
	wantError(t, err, ErrBusy)
	// Simulate worker completion, not browser detach or lease expiration.
	_, err = l.Store.db.Exec(`UPDATE turns SET disposition='consumed' WHERE disposition='pending' AND queue_policy='released'`)
	must(t, err)
	term, err := l.Store.CreateTerminal(ctx, a, "terminal", "Shell", 24, 80)
	must(t, err)
	_, err = l.Store.ArmIdle(ctx, a, time.Second)
	must(t, err) // row alone isn't live.
	must(t, l.Store.BeginActivity(ctx, a, Activity{ID: "start", TerminalID: term.ID, Kind: "terminal_start"}))
	i, err := l.Store.reserveTerminal(ctx, a, term.ID, 24, 80)
	must(t, err)
	must(t, l.Store.TerminalRunning(ctx, i))
	must(t, l.Store.SettleActivity(ctx, a.ProjectID, "start", "complete"))
	// The live PTY process is the blocker, not the Terminal row and not the
	// settled start activity.
	bs, err = l.Store.Blockers(ctx, a)
	must(t, err)
	if len(bs) != 1 || bs[0].Kind != "terminal_process" {
		t.Fatal(bs)
	}
	_, err = l.Store.ArmIdle(ctx, a, time.Second)
	wantError(t, err, ErrBusy)
	// A silent terminal has no browser state to clear. Only its observed exit
	// settles it, and the recorded code survives.
	must(t, l.Store.SettleTerminalExit(ctx, i, 0))
	settled, err := l.Store.Terminal(ctx, a, term.ID)
	must(t, err)
	if settled.State != "exited" || settled.ExitCode == nil || *settled.ExitCode != 0 {
		t.Fatal(settled)
	}
	timer, err = l.Store.ArmIdle(ctx, a, time.Second)
	must(t, err)
	now = now.Add(2 * time.Second)
	must(t, l.PauseIdle(ctx, a, timer))
	drive(t, l, a.ProjectID)
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	if p.State != "paused" {
		t.Fatal(p)
	}
}

func TestLostPauseResumeAndAdmissionDuringPause(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	_, err := l.Store.CreateSession(ctx, a, "s", "Agent")
	must(t, err)
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	must(t, l.Pause(ctx, a, p.Revision))
	_, err = l.Store.AdmitTurn(ctx, a, "s", "r", "during pause", false)
	must(t, err)
	f.fail["pause"] = 1
	if err = l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("lost pause reply missing")
	}
	r, err := l.Store.Resources(ctx, a)
	must(t, err)
	if !r.Occupied {
		t.Fatal("released uncertain placement")
	}
	drive(t, l, a.ProjectID)
	r, err = l.Store.Resources(ctx, a)
	must(t, err)
	if r.Occupied {
		t.Fatal("confirmed pause retains placement")
	}
	must(t, l.EnsureActive(ctx, a, testProfile()))
	f.fail["resume"] = 1
	if err = l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("lost resume reply missing")
	}
	drive(t, l, a.ProjectID)
	r, err = l.ActiveResources(ctx, a)
	must(t, err)
	if r.Reference.Epoch != 2 || !r.Occupied || f.calls["pause"] != 1 || f.calls["resume"] != 1 {
		t.Fatal(r, f.calls)
	}
	turns, err := l.Store.Turns(ctx, a, "s", 0, 100)
	must(t, err)
	if len(turns) != 1 || turns[0].State != "queued" {
		t.Fatal(turns)
	}
}

func TestRestartRecoversDeadlineAndActivities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.db")
	s, err := openFixtureStore(ctx, path)
	must(t, err)
	f := fakeBackend()
	l := &Lifecycle{Store: s, Backend: f, MaxProjects: 1}
	a := Scope{"alice", "a"}
	_, err = s.CreateProject(ctx, a.Actor, a.ProjectID, "A")
	must(t, err)
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	timer, err := s.ArmIdle(ctx, a, time.Second)
	must(t, err)
	must(t, s.Close())
	s, err = openFixtureStore(ctx, path)
	must(t, err)
	defer s.Close()
	l = &Lifecycle{Store: s, Backend: f, MaxProjects: 1}
	s.now = func() time.Time { return time.Now().Add(time.Minute) }
	must(t, l.PauseIdle(ctx, a, timer))
	drive(t, l, a.ProjectID)
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	must(t, s.BeginActivity(ctx, a, Activity{ID: "upload", Kind: "files", Detail: "large upload"}))
	must(t, s.SettleActivity(ctx, a.ProjectID, "upload", "unknown"))
	_, err = s.ArmIdle(ctx, a, time.Second)
	wantError(t, err, ErrBusy)
	must(t, s.SettleActivity(ctx, a.ProjectID, "upload", "complete"))
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, l.Restart(ctx, a, p.Revision))
	drive(t, l, a.ProjectID)
	r, err := l.ActiveResources(ctx, a)
	must(t, err)
	if r.Reference.Epoch != 3 {
		t.Fatal(r)
	}
}

func TestDeleteAfterUncertainProvisioningReclaimsOwnedResources(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile()))
	must(t, l.Reconcile(ctx, a.ProjectID))
	f.fail["volume"] = 1
	if err := l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("expected unknown runtime volume")
	}
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	must(t, l.Store.DeleteProject(ctx, a, p.Revision))
	f.fail["destroy"] = 1
	f.fail["delete-volume"] = 1
	for i := 0; i < 20; i++ {
		_ = l.Reconcile(ctx, a.ProjectID)
		p, err = l.Store.Project(ctx, a)
		must(t, err)
		if p.State == "deleted" {
			break
		}
	}
	if p.State != "deleted" || len(f.sandboxes) != 0 || len(f.volumes) != 0 {
		t.Fatal(p, f.sandboxes, f.volumes)
	}
	r, err := l.Store.Resources(ctx, a)
	must(t, err)
	if r.Occupied {
		t.Fatal(r)
	}
	wantError(t, l.EnsureActive(ctx, a, testProfile()), ErrDeleting)
	pending, err := l.Store.PendingProjects(ctx, 100)
	must(t, err)
	if len(pending) != 0 {
		t.Fatal(pending)
	}
}

func TestIdentityMismatchAndChangedConditionalReferenceStayFenced(t *testing.T) {
	l, a, f := lifecycleFixture(t)
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	p, err := l.Store.Project(ctx, a)
	must(t, err)
	must(t, l.Pause(ctx, a, p.Revision))
	r, err := l.Store.Resources(ctx, a)
	must(t, err)
	f.store = "replacement"
	if err = l.Reconcile(ctx, a.ProjectID); err == nil {
		t.Fatal("replacement engine accepted")
	}
	f.store = "engine"
	obs := f.sandboxes[r.Reference.SandboxID]
	obs.MutationRevision = "operator-changed"
	f.sandboxes[r.Reference.SandboxID] = obs
	wantError(t, l.Reconcile(ctx, a.ProjectID), ErrConflict)
	if f.calls["pause"] != 0 {
		t.Fatal("old operation adopted new revision")
	}
	r, err = l.Store.Resources(ctx, a)
	must(t, err)
	if !r.Occupied {
		t.Fatal("uncertainty released capacity")
	}
}

func (f *fakeLifecycle) InspectConfiguration(_ context.Context, ref sandbox.Reference) (sandbox.Configuration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.sandboxes[ref.SandboxID]
	if !ok {
		return sandbox.Configuration{}, &sandbox.Error{Class: sandbox.NotFound}
	}
	r := f.configurations[ref.SandboxID]
	return sandbox.Configuration{Observation: o, Network: r.Network, Mounts: r.Mounts, TemplateID: r.TemplateID, CPUMillis: r.CPUMillis, MemoryBytes: r.MemoryBytes, DiskCapacityBytes: r.DiskBytes, IdleTimeout: "0s", Retention: "0s"}, nil
}
func (f *fakeLifecycle) UpdateNetwork(ctx context.Context, ref sandbox.Reference, network sandbox.Network) (sandbox.Configuration, error) {
	f.mu.Lock()
	o := f.sandboxes[ref.SandboxID]
	if o.Reference != ref || o.State != "paused" {
		f.mu.Unlock()
		return sandbox.Configuration{}, ErrConflict
	}
	r := f.configurations[ref.SandboxID]
	r.Network = network
	f.configurations[ref.SandboxID] = r
	o.MutationRevision += "n"
	f.sandboxes[ref.SandboxID] = o
	err := f.lost("network")
	f.mu.Unlock()
	if err != nil {
		return sandbox.Configuration{}, err
	}
	return f.InspectConfiguration(ctx, ref)
}
