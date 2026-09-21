package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/sandbox"
)

var ErrPending = errors.New("project operation pending")
var ErrCapacity = errors.New("active Project capacity exhausted")

type Profile struct {
	ReleaseDigest   string          `json:"release_digest,omitempty"`
	StoreID         string          `json:"store_id"`
	TemplateID      string          `json:"template_id"`
	CPUMillis       int64           `json:"cpu_millis"`
	MemoryBytes     int64           `json:"memory_bytes"`
	DiskBytes       int64           `json:"disk_bytes"`
	WorkspaceBytes  int64           `json:"workspace_bytes"`
	RuntimeBytes    int64           `json:"runtime_bytes"`
	Reachability    Reachability    `json:"reachability"`
	PublishedMounts []sandbox.Mount `json:"published_mounts"`
}

func (p Profile) Validate() error {
	if p.ReleaseDigest != "" && !content.ValidDigest(p.ReleaseDigest) {
		return ErrInvalid
	}
	if p.StoreID == "" || p.TemplateID == "" || p.CPUMillis <= 0 || p.MemoryBytes <= 0 || p.DiskBytes <= 0 || p.WorkspaceBytes <= 0 || p.RuntimeBytes <= 0 {
		return ErrInvalid
	}
	for _, m := range p.PublishedMounts {
		if m.Access != "shared-ro" || m.VolumeID == "" || (m.Destination != "/opt/agent-config" && m.Destination != "/opt/shared-assets") {
			return ErrInvalid
		}
	}
	return p.Reachability.validate()
}

type Resources struct {
	ProjectID       string            `json:"project_id"`
	Reference       sandbox.Reference `json:"reference"`
	Profile         Profile           `json:"profile"`
	WorkspaceVolume string            `json:"workspace_volume"`
	RuntimeVolume   string            `json:"runtime_volume"`
	Occupied        bool              `json:"occupied"`
}

func resources(ctx context.Context, q queryer, project string) (r Resources, err error) {
	var raw string
	err = missing(q.QueryRowContext(ctx, `SELECT project_id,store_id,profile,workspace_volume,runtime_volume,sandbox_id,mutation_revision,epoch,occupied FROM project_resources WHERE project_id=?`, project).Scan(&r.ProjectID, &r.Reference.StoreID, &raw, &r.WorkspaceVolume, &r.RuntimeVolume, &r.Reference.SandboxID, &r.Reference.MutationRevision, &r.Reference.Epoch, &r.Occupied))
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r.Profile)
	}
	return
}

// resourcesFor reads a Project's binding without an actor scope. Workers use it:
// reconciliation must keep running after user admission closed.
func (s *Store) resourcesFor(ctx context.Context, project string) (Resources, error) {
	return resources(ctx, s.reader, project)
}
func (s *Store) Resources(ctx context.Context, scope Scope) (Resources, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return Resources{}, err
	}
	return resources(ctx, s.reader, scope.ProjectID)
}

type LifecycleBackend interface {
	Identity(context.Context) (sandbox.Identity, error)
	CreateVolume(context.Context, string, int64) (sandbox.Volume, error)
	Create(context.Context, sandbox.CreateRequest) (sandbox.Observation, error)
	Inspect(context.Context, sandbox.Reference) (sandbox.Observation, error)
	Pause(context.Context, sandbox.Reference) (sandbox.Observation, error)
	Resume(context.Context, sandbox.Reference) (sandbox.Observation, error)
	Destroy(context.Context, sandbox.Reference) error
	DeleteVolume(context.Context, sandbox.VolumeReference) error
	// WriteProjection materializes a host-owned guest file for a placement. Only
	// the lifecycle calls it, and only from the immutable profile, so a child
	// start can never rewrite reachability that a sibling depends on.
	WriteProjection(context.Context, sandbox.Reference, string, []byte) error
}

// Lifecycle is the sole Project placement authority. Fixed locks bound memory;
// durable operation lanes supply restart discovery, not active-active ownership.
type Lifecycle struct {
	VerifyRuntime func(context.Context, sandbox.Reference, string) error
	Artifacts     ProjectArtifacts
	Store         *Store
	Backend       LifecycleBackend
	MaxProjects   int
	lanes         [64]sync.Mutex
}

func (l *Lifecycle) lane(id string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(id))
	return &l.lanes[h.Sum32()%uint32(len(l.lanes))]
}

type operationRequest struct {
	Reference sandbox.Reference
	Restart   bool
	Force     bool
}
type operation struct{ ID, ProjectID, Kind, State, Phase, Request, Error string }

func pendingOperation(ctx context.Context, q queryer, project string) (o operation, err error) {
	err = missing(q.QueryRowContext(ctx, `SELECT id,project_id,kind,state,phase,request,error FROM project_operations WHERE project_id=? AND state<>'succeeded'`, project).Scan(&o.ID, &o.ProjectID, &o.Kind, &o.State, &o.Phase, &o.Request, &o.Error))
	return
}
func (l *Lifecycle) reserve(ctx context.Context, tx *sql.Tx) error {
	if l.MaxProjects < 1 {
		return ErrCapacity
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM project_resources WHERE occupied=1`).Scan(&n); err != nil {
		return err
	}
	if n >= l.MaxProjects {
		return ErrCapacity
	}
	return nil
}
func (l *Lifecycle) newOperation(ctx context.Context, tx *sql.Tx, project, kind, phase string, request operationRequest) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	now := l.Store.timestamp()
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_operations(id,project_id,kind,phase,request,created_at,attempted_at) VALUES(?,?,?,?,?,?,?)`, uuid.NewString(), project, kind, phase, string(raw), now, now); err != nil {
		return err
	}
	state := map[string]string{"create": "provisioning", "resume": "resuming", "pause": "pausing", "delete": "deleting"}[kind]
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET state=?,failure='',idle_deadline=NULL WHERE id=?`, state, project); err != nil {
		return err
	}
	return l.Store.bumpProject(ctx, tx, project, "project."+state)
}

// EnsureActive records original provisioning/resume intent. It does not issue
// remote work or tie its lifetime to an HTTP request. Workers call Reconcile.
func (l *Lifecycle) EnsureActive(ctx context.Context, scope Scope, profile Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		p, err := writableProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		r, err := resources(ctx, tx, p.ID)
		if errors.Is(err, ErrNotFound) {
			if err = l.reserve(ctx, tx); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO project_resources(project_id,store_id,profile,occupied) VALUES(?,?,?,1)`, p.ID, profile.StoreID, string(raw)); err != nil {
				return err
			}
			return l.newOperation(ctx, tx, p.ID, "create", "workspace", operationRequest{})
		}
		if err != nil {
			return err
		}
		stored, err := json.Marshal(r.Profile)
		if err != nil {
			return err
		}
		if string(stored) != string(raw) {
			return ErrConflict
		}
		if _, err := pendingOperation(ctx, tx, p.ID); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if p.State == "active" {
			return nil
		}
		if p.State != "paused" {
			return ErrConflict
		}
		if err = l.reserve(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE project_resources SET occupied=1 WHERE project_id=?`, p.ID); err != nil {
			return err
		}
		return l.newOperation(ctx, tx, p.ID, "resume", "sandbox", operationRequest{Reference: r.Reference})
	})
}
func (l *Lifecycle) ActiveResources(ctx context.Context, scope Scope) (Resources, error) {
	p, err := l.Store.Project(ctx, scope)
	if err != nil {
		return Resources{}, err
	}
	if p.State == "deleting" || p.State == "deleted" {
		return Resources{}, ErrDeleting
	}
	if p.State != "active" {
		return Resources{}, ErrPending
	}
	return l.Store.Resources(ctx, scope)
}
func (l *Lifecycle) Pause(ctx context.Context, scope Scope, revision int64) error {
	return l.requestPause(ctx, scope, revision, nil, false)
}
func (l *Lifecycle) Restart(ctx context.Context, scope Scope, revision int64) error {
	return l.requestPause(ctx, scope, revision, nil, true)
}

// PauseIdle accepts only the exact armed timer. New admission clears it in the
// same transaction that makes its turn/activity durable.
func (l *Lifecycle) PauseIdle(ctx context.Context, scope Scope, snapshot Project) error {
	return l.requestPause(ctx, scope, snapshot.Revision, &snapshot, false)
}
func (l *Lifecycle) requestPause(ctx context.Context, scope Scope, revision int64, idle *Project, restart bool) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		p, err := writableProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if p.Revision != revision || p.State != "active" {
			return ErrConflict
		}
		if idle != nil && (p.ActivityRevision != idle.ActivityRevision || p.IdleDeadline == "" || p.IdleDeadline != idle.IdleDeadline || p.IdleDeadline > l.Store.timestamp()) {
			return ErrConflict
		}
		bs, err := blockers(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if len(bs) > 0 {
			return ErrBusy
		}
		if _, err := pendingOperation(ctx, tx, p.ID); err == nil {
			return ErrPending
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		r, err := resources(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		return l.newOperation(ctx, tx, p.ID, "pause", "sandbox", operationRequest{Reference: r.Reference, Restart: restart})
	})
}

// Reconcile performs at most one remote phase. Fair workers page pending
// Projects; one failed volume/export cannot monopolize the whole loop.
func (l *Lifecycle) Reconcile(ctx context.Context, project string) error {
	lock := l.lane(project)
	lock.Lock()
	defer lock.Unlock()
	var o operation
	var r Resources
	err := l.Store.write(ctx, func(tx *sql.Tx) error {
		var err error
		o, err = pendingOperation(ctx, tx, project)
		if errors.Is(err, ErrNotFound) {
			var state string
			if err = tx.QueryRowContext(ctx, `SELECT state FROM projects WHERE id=?`, project).Scan(&state); err != nil {
				return missing(err)
			}
			if state != "deleting" {
				return ErrNotFound
			}
			r, err = resources(ctx, tx, project)
			if errors.Is(err, ErrNotFound) {
				if err = l.newOperation(ctx, tx, project, "delete", "complete", operationRequest{}); err != nil {
					return err
				}
				o, err = pendingOperation(ctx, tx, project)
				r.ProjectID = project
				return err
			}
			if err != nil {
				return err
			}
			if err = l.newOperation(ctx, tx, project, "delete", "sandbox", operationRequest{Reference: r.Reference}); err != nil {
				return err
			}
			o, err = pendingOperation(ctx, tx, project)
		}
		if err != nil {
			return err
		}
		r, err = resources(ctx, tx, project)
		if errors.Is(err, ErrNotFound) && o.Kind == "delete" && o.Phase == "complete" {
			r.ProjectID = project
			err = nil
		}
		if err != nil {
			return err
		}
		if o.Kind == "create" && !r.Occupied {
			if err = l.reserve(ctx, tx); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE project_resources SET occupied=1 WHERE project_id=?`, project); err != nil {
				return err
			}
			r.Occupied = true
		}
		_, err = tx.ExecContext(ctx, `UPDATE project_operations SET state='issued',attempted_at=? WHERE id=?`, l.Store.timestamp(), o.ID)
		return err
	})
	if err != nil {
		return err
	}
	if o.ID == "" {
		return nil
	}
	if o.Kind == "delete" && o.Phase == "complete" {
		err := l.destroy(ctx, o, r)
		if err != nil {
			return l.unknown(ctx, o, err)
		}
		return nil
	}
	identity, err := l.Backend.Identity(ctx)
	if err == nil && identity.StoreID != r.Reference.StoreID {
		err = fmt.Errorf("engine store changed: original %s", r.Reference.StoreID)
	}
	if err != nil {
		return l.unknown(ctx, o, err)
	}
	switch {
	// A committed placement awaiting its guest projection is resumed by phase, not
	// by kind: create and resume converge on the same remaining work.
	case o.Phase == "reachability" && (o.Kind == "create" || o.Kind == "resume"):
		err = l.reprojectReachability(ctx, o, r)
	case o.Kind == "create":
		err = l.provision(ctx, o, r)
	case o.Kind == "pause" || o.Kind == "resume":
		err = l.transition(ctx, o, r)
	case o.Kind == "delete":
		err = l.destroy(ctx, o, r)
	default:
		err = ErrInvalid
	}
	if err != nil {
		return l.unknown(ctx, o, err)
	}
	return nil
}
func (l *Lifecycle) unknown(ctx context.Context, o operation, cause error) error {
	settle, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	err := l.Store.write(settle, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(settle, `UPDATE project_operations SET state='unknown',error=? WHERE id=? AND state<>'succeeded'`, cause.Error(), o.ID); err != nil {
			return err
		}
		res, err := tx.ExecContext(settle, `UPDATE projects SET failure=? WHERE id=? AND failure<>?`, cause.Error(), o.ProjectID, cause.Error())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			return l.Store.bumpProject(settle, tx, o.ProjectID, "project.operation_unknown")
		}
		return nil
	})
	return errors.Join(cause, err)
}
func (l *Lifecycle) phase(ctx context.Context, o operation, next, column, value string) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		if column != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET `+column+`=? WHERE project_id=?`, value, o.ProjectID); err != nil {
				return err
			}
		}
		return changed(tx.ExecContext(ctx, `UPDATE project_operations SET phase=?,state='pending',error='' WHERE id=? AND phase=? AND state<>'succeeded'`, next, o.ID, o.Phase))
	})
}
func (l *Lifecycle) provision(ctx context.Context, o operation, r Resources) error {
	switch o.Phase {
	case "workspace", "runtime":
		capacity := r.Profile.WorkspaceBytes
		column, next := "workspace_volume", "runtime"
		if o.Phase == "runtime" {
			capacity = r.Profile.RuntimeBytes
			column, next = "runtime_volume", "sandbox"
		}
		v, err := l.Backend.CreateVolume(ctx, o.ID+"/"+o.Phase, capacity)
		if err != nil {
			return err
		}
		if v.StoreID != r.Reference.StoreID || v.VolumeID == "" || v.Capacity != capacity {
			return ErrConflict
		}
		return l.phase(ctx, o, next, column, v.VolumeID)
	case "sandbox":
		p := r.Profile
		mounts := append([]sandbox.Mount{}, p.PublishedMounts...)
		mounts = append(mounts, sandbox.Mount{VolumeID: r.WorkspaceVolume, Destination: "/workspace", Access: "exclusive"}, sandbox.Mount{VolumeID: r.RuntimeVolume, Destination: "/runtime", Access: "exclusive"})
		observed, err := l.Backend.Create(ctx, sandbox.CreateRequest{IdempotencyKey: o.ID + "/sandbox", TemplateID: p.TemplateID, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, DiskBytes: p.DiskBytes, Network: p.Reachability.Declaration(), Mounts: mounts})
		if err != nil {
			return err
		}
		if observed.StoreID != r.Reference.StoreID || observed.SandboxID == "" || observed.Epoch == 0 || observed.MutationRevision == "" || observed.State != "running" || observed.PlacementState != "active" {
			return ErrConflict
		}
		// The binding identity is committed before the projection is written, so a
		// lost reply here retries the projection against the original placement
		// rather than creating a second sandbox.
		if err = l.commit(ctx, o, observed, "reachability"); err != nil {
			return err
		}
		o.Phase = "reachability"
		return l.reachability(ctx, o, r.Profile, observed.Reference)
	default:
		return ErrInvalid
	}
}

// reprojectReachability retries the guest projection for an already committed
// placement, after a crash or a lost reply. It re-inspects rather than trusting
// the stored epoch, so it can never write into a successor placement.
func (l *Lifecycle) reprojectReachability(ctx context.Context, o operation, r Resources) error {
	observed, err := l.Backend.Inspect(ctx, r.Reference)
	if err != nil {
		return err
	}
	if observed.SandboxID != r.Reference.SandboxID || observed.Epoch != r.Reference.Epoch || observed.MutationRevision != r.Reference.MutationRevision || observed.State != "running" || observed.PlacementState != "active" {
		return ErrConflict
	}
	return l.reachability(ctx, o, r.Profile, r.Reference)
}

// reachability writes the Project's host-owned guest projections for one
// placement. It is idempotent: the bytes derive only from the immutable profile,
// so a retry after a lost reply writes identical content.
func (l *Lifecycle) reachability(ctx context.Context, o operation, p Profile, ref sandbox.Reference) error {
	for _, projection := range []struct{ path, content string }{
		{"/etc/hosts", p.Reachability.HostsProjection()},
		{"/etc/resolv.conf", p.Reachability.ResolverProjection()},
	} {
		if err := l.Backend.WriteProjection(ctx, ref, projection.path, []byte(projection.content)); err != nil {
			return err
		}
	}
	if p.ReleaseDigest != "" {
		if l.VerifyRuntime == nil {
			return ErrInvalid
		}
		if err := l.VerifyRuntime(ctx, ref, p.ReleaseDigest); err != nil {
			return err
		}
	}
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		return l.settle(ctx, tx, o, "active", true, false)
	})
}
func (l *Lifecycle) transition(ctx context.Context, o operation, r Resources) error {
	var request operationRequest
	if err := json.Unmarshal([]byte(o.Request), &request); err != nil {
		return err
	}
	ref := request.Reference
	observed, err := l.Backend.Inspect(ctx, ref)
	if err != nil {
		return err
	}
	if observed.StoreID != ref.StoreID || observed.SandboxID != ref.SandboxID {
		return ErrConflict
	}
	if o.Kind == "pause" {
		if observed.State == "running" && observed.Epoch == ref.Epoch && observed.MutationRevision == ref.MutationRevision {
			observed, err = l.Backend.Pause(ctx, ref)
			if err != nil {
				return err
			}
		}
		if observed.StoreID != ref.StoreID || observed.SandboxID != ref.SandboxID || observed.State != "paused" || observed.PlacementState != "lost" || observed.Epoch != ref.Epoch {
			return ErrConflict
		}
		return l.finish(ctx, o, observed, "paused", request.Restart, request.Restart)
	}
	if observed.State == "paused" && observed.Epoch == ref.Epoch && observed.MutationRevision == ref.MutationRevision {
		observed, err = l.Backend.Resume(ctx, ref)
		if err != nil {
			return err
		}
	}
	if observed.StoreID != ref.StoreID || observed.SandboxID != ref.SandboxID || observed.State != "running" || observed.PlacementState != "active" || observed.Epoch != ref.Epoch+1 {
		return ErrConflict
	}
	// A resumed placement is a fresh guest filesystem for host-owned projections:
	// the engine rebuilds network rules from the stored declaration, but /etc
	// content lives in the template layer and must be rewritten for this epoch.
	if err = l.commit(ctx, o, observed, "reachability"); err != nil {
		return err
	}
	o.Phase = "reachability"
	return l.reachability(ctx, o, r.Profile, observed.Reference)
}

// commit records the observed placement binding and advances the operation to a
// later phase without completing it. The binding must be durable before any
// further remote work so a lost reply retries that work, not the placement.
func (l *Lifecycle) commit(ctx context.Context, o operation, obs sandbox.Observation, next string) error {
	if obs.MutationRevision == "" {
		return ErrConflict
	}
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET sandbox_id=?,mutation_revision=?,epoch=?,occupied=1 WHERE project_id=?`, obs.SandboxID, obs.MutationRevision, obs.Epoch, o.ProjectID); err != nil {
			return err
		}
		if err := l.Store.loseStaleAgents(ctx, tx, o.ProjectID, obs.Epoch, "placement replaced; Agent process did not survive"); err != nil {
			return err
		}
		// A new epoch is a new guest: any PTY recorded against the old placement is
		// gone. Settling here, in the transaction that commits the epoch, means no
		// window exists where a dead process still pins an idle Project.
		if _, err := l.Store.loseStalePTYs(ctx, tx, o.ProjectID, obs.Epoch, "placement replaced; PTY process did not survive"); err != nil {
			return err
		}
		return changed(tx.ExecContext(ctx, `UPDATE project_operations SET phase=?,state='pending',error='' WHERE id=? AND phase=? AND state<>'succeeded'`, next, o.ID, o.Phase))
	})
}
func (l *Lifecycle) finish(ctx context.Context, o operation, obs sandbox.Observation, state string, occupied, restart bool) error {
	return l.Store.write(ctx, func(tx *sql.Tx) error {
		if obs.MutationRevision == "" {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET sandbox_id=?,mutation_revision=?,epoch=?,occupied=? WHERE project_id=?`, obs.SandboxID, obs.MutationRevision, obs.Epoch, occupied, o.ProjectID); err != nil {
			return err
		}
		// A paused placement holds no processes. Any PTY still recorded as running
		// is lost, with no exit code: it did not exit, it became unobservable.
		// Pause keeps the epoch, so "current" cannot be the criterion here; epoch 0
		// is not a legal incarnation epoch, so every live PTY qualifies as stale.
		current, reason := obs.Epoch, "placement replaced; PTY process did not survive"
		if state == "paused" {
			current, reason = 0, "Project paused; PTY process did not survive"
		}
		if err := l.Store.loseStaleAgents(ctx, tx, o.ProjectID, current, reason); err != nil {
			return err
		}
		if _, err := l.Store.loseStalePTYs(ctx, tx, o.ProjectID, current, reason); err != nil {
			return err
		}
		if state == "paused" {
			if err := l.Store.settleLostActivities(ctx, tx, o.ProjectID); err != nil {
				return err
			}
		}
		return l.settle(ctx, tx, o, state, occupied, restart, obs.Reference)
	})
}
func (l *Lifecycle) settle(ctx context.Context, tx *sql.Tx, o operation, state string, occupied, restart bool, resume ...sandbox.Reference) error {
	if err := changed(tx.ExecContext(ctx, `UPDATE project_operations SET state='succeeded',error='' WHERE id=? AND state<>'succeeded'`, o.ID)); err != nil {
		return err
	}
	var deleting bool
	if err := tx.QueryRowContext(ctx, `SELECT state='deleting' FROM projects WHERE id=?`, o.ProjectID).Scan(&deleting); err != nil {
		return err
	}
	if deleting {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET state=?,failure='' WHERE id=?`, state, o.ProjectID); err != nil {
		return err
	}
	if err := l.Store.bumpProject(ctx, tx, o.ProjectID, "project."+state); err != nil {
		return err
	}
	if restart && len(resume) == 1 {
		return l.newOperation(ctx, tx, o.ProjectID, "resume", "sandbox", operationRequest{Reference: resume[0]})
	}
	return nil
}
func (l *Lifecycle) destroy(ctx context.Context, o operation, r Resources) error {
	switch o.Phase {
	case "sandbox":
		if r.Reference.SandboxID != "" {
			if err := l.Backend.Destroy(ctx, r.Reference); err != nil {
				return err
			}
		}
		// The original binding remains as tombstone evidence until all storage is
		// reclaimed; only confirmed sandbox absence releases placement capacity.
		return l.Store.write(ctx, func(tx *sql.Tx) error {
			if err := l.Store.settleDestroyedPlacement(ctx, tx, o.ProjectID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_resources SET occupied=0 WHERE project_id=?`, o.ProjectID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE project_operations SET phase='workspace',state='pending',error='' WHERE id=?`, o.ID)
			return err
		})
	case "workspace", "runtime":
		id, next := r.WorkspaceVolume, "runtime"
		if o.Phase == "runtime" {
			id, next = r.RuntimeVolume, "complete"
		}
		if id != "" {
			if err := l.Backend.DeleteVolume(ctx, sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: id}); err != nil {
				return err
			}
		}
		return l.phase(ctx, o, next, "", "")
	case "complete":
		if l.Artifacts != nil {
			if err := l.Artifacts.RemoveProject(ctx, o.ProjectID); err != nil {
				return err
			}
		} else {
			var pending int
			if err := l.Store.reader.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM shares WHERE project_id=? AND cleaned=0)+(SELECT count(*) FROM native_captures c JOIN agent_incarnations i ON i.id=c.incarnation_id WHERE i.project_id=? AND c.host_path<>'')`, o.ProjectID, o.ProjectID).Scan(&pending); err != nil {
				return err
			}
			if pending > 0 {
				return ErrPending
			}
		}
		return l.Store.write(ctx, func(tx *sql.Tx) error {
			if err := l.Store.purgeProject(ctx, tx, o.ProjectID); err != nil {
				return err
			}

			if _, err := tx.ExecContext(ctx, `UPDATE project_operations SET state='succeeded',error='' WHERE id=?`, o.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE projects SET state='deleted',failure='' WHERE id=?`, o.ProjectID); err != nil {
				return err
			}
			return l.Store.bumpProject(ctx, tx, o.ProjectID, "project.deleted")
		})
	default:
		return ErrInvalid
	}
}

// PendingProjects rotates attempts by oldest attempt, and includes deletion
// tombstones even when no remote operation had yet been created at a crash.
func (s *Store) PendingProjects(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT p.id FROM projects p LEFT JOIN project_operations o ON o.project_id=p.id AND o.state<>'succeeded' WHERE o.id IS NOT NULL OR p.state='deleting' ORDER BY coalesce(o.attempted_at,p.updated_at),p.id LIMIT ?`, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
