package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
)

// VolumeSource is a resolved immutable artifact binding, never browser input.
// Its evidence stays with the saved plan even after the operator changes defaults.
type VolumeSource struct {
	Reference    ArtifactReference `json:"reference"`
	VolumeID     string            `json:"volume_id"`
	MinimumBytes int64             `json:"minimum_bytes"`
	CleanRuntime bool              `json:"clean_runtime"`
}
type VolumePlan struct {
	Key           string        `json:"key"`
	Role          string        `json:"role"`
	Destination   string        `json:"destination"`
	Storage       string        `json:"storage"`
	Access        string        `json:"access"`
	CapacityBytes int64         `json:"capacity_bytes"`
	Source        *VolumeSource `json:"source,omitempty"`
}
type VolumeSlot struct {
	Key       string `json:"key"`
	VolumeID  string `json:"volume_id,omitempty"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	IntentKey string `json:"-"`
}

func (p Profile) VolumeForRole(role string) (VolumePlan, bool) {
	for _, plan := range p.Volumes {
		if plan.Role == role {
			return plan, true
		}
	}
	return VolumePlan{}, false
}
func (p Profile) CapacityForRole(role string) int64 {
	plan, _ := p.VolumeForRole(role)
	return plan.CapacityBytes
}
func (p Profile) RolePath(role string) string {
	plan, _ := p.VolumeForRole(role)
	return plan.Destination
}

// RunnerPaths derives every guest role root from the immutable saved plan.
func (p Profile) RunnerPaths() []string {
	return []string{"--workspace", p.RolePath("workspace"), "--state-root", p.RolePath("runtime"), "--config-root", p.RolePath("agent_config"), "--shared-root", p.RolePath("shared_assets")}
}
func (r Resources) VolumeForRole(role string) string {
	plan, ok := r.Profile.VolumeForRole(role)
	if !ok {
		return ""
	}
	for _, slot := range r.Volumes {
		if slot.Key == plan.Key && slot.State == "ready" {
			return slot.VolumeID
		}
	}
	return ""
}
func (r Resources) Mounts() ([]sandbox.Mount, error) {
	mounts := make([]sandbox.Mount, 0, len(r.Profile.Volumes))
	for _, plan := range r.Profile.Volumes {
		var selected *VolumeSlot
		for index := range r.Volumes {
			if r.Volumes[index].Key == plan.Key {
				selected = &r.Volumes[index]
				break
			}
		}
		if selected == nil || selected.State != "ready" || selected.VolumeID == "" {
			return nil, ErrPending
		}
		access := "exclusive"
		if plan.Access == "read_only" {
			access = "shared-ro"
		}
		mounts = append(mounts, sandbox.Mount{VolumeID: selected.VolumeID, Destination: plan.Destination, Access: access})
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Destination < mounts[j].Destination })
	return mounts, nil
}
func volumeSlots(ctx context.Context, q queryer, project string) ([]VolumeSlot, error) {
	rows, err := q.QueryContext(ctx, `SELECT slot_key,volume_id,state,error,intent_key FROM project_volume_slots WHERE project_id=? ORDER BY slot_key`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VolumeSlot{}
	for rows.Next() {
		var slot VolumeSlot
		if err = rows.Scan(&slot.Key, &slot.VolumeID, &slot.State, &slot.Error, &slot.IntentKey); err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}
func canonicalProfile(profile Profile) Profile {
	profile.Volumes = append([]VolumePlan(nil), profile.Volumes...)
	sort.Slice(profile.Volumes, func(i, j int) bool { return profile.Volumes[i].Key < profile.Volumes[j].Key })
	return profile
}
func (s *Store) bindVolumePlan(ctx context.Context, tx *sql.Tx, project string, profile Profile, occupied bool) error {
	profile = canonicalProfile(profile)
	var actor string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, project).Scan(&actor); err != nil {
		return err
	}
	if err := activeAccount(ctx, tx, actor); err != nil {
		return err
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if err := admitStoragePlan(ctx, tx, actor, profile); err != nil {
		return err
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_resources(project_id,store_id,profile,occupied) VALUES(?,?,?,?)`, project, profile.StoreID, string(raw), occupied); err != nil {
		return err
	}
	for _, plan := range profile.Volumes {
		id, state := "", "reserved"
		if plan.Storage == "shared_readonly" {
			id, state = plan.Source.VolumeID, "ready"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO project_volume_slots(project_id,slot_key,store_id,storage,intent_key,volume_id,state) VALUES(?,?,?,?,?,?,?)`, project, plan.Key, profile.StoreID, plan.Storage, "volume-"+uuid.NewString(), id, state); err != nil {
			return err
		}
		if plan.Storage == "owned" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO storage_reservations(project_id,slot_key,capacity,state) VALUES(?,?,?,'reserved')`, project, plan.Key, plan.CapacityBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

// materializeSlot uses the same durable key for initial upload, first compute,
// lost replies and cleanup reconciliation. Caller holds the Project lifecycle lane.
func (l *Lifecycle) materializeSlot(ctx context.Context, r Resources, key string) (ready bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, l.Store.recordSlotFailure(ctx, r.ProjectID, key, resultErr))
		}
	}()
	var plan VolumePlan
	found := false
	for _, candidate := range r.Profile.Volumes {
		if candidate.Key == key {
			plan = candidate
			found = true
			break
		}
	}
	if !found {
		return false, ErrInvalid
	}
	var slot VolumeSlot
	for _, candidate := range r.Volumes {
		if candidate.Key == key {
			slot = candidate
			break
		}
	}
	if slot.Key == "" {
		return false, ErrConflict
	}
	if slot.State == "ready" {
		return true, nil
	}
	if plan.Storage != "owned" || slot.State == "deleted" {
		return false, ErrConflict
	}
	if slot.State == "reserved" {
		if err := l.Store.write(ctx, func(tx *sql.Tx) error {
			var actor string
			if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id=?`, r.ProjectID).Scan(&actor); err != nil {
				return err
			}
			if err := admitReservedStorage(ctx, tx, actor, r.Profile); err != nil {
				return err
			}
			return changed(tx.ExecContext(ctx, `UPDATE project_volume_slots SET state='creating' WHERE project_id=? AND slot_key=? AND state='reserved'`, r.ProjectID, key))
		}); err != nil {
			return false, err
		}
		slot.State = "creating"
	}
	if slot.State == "creating" {
		var volume sandbox.Volume
		var err error
		if plan.Source == nil {
			volume, err = l.Backend.CreateVolume(ctx, slot.IntentKey, plan.CapacityBytes)
		} else {
			seed, ok := l.Backend.(sandbox.VolumeDerivation)
			if !ok {
				return false, Admission("artifact_unavailable", ErrInvalid)
			}
			volume, err = seed.SnapshotVolume(ctx, sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: plan.Source.VolumeID}, slot.IntentKey, plan.CapacityBytes)
		}
		if err != nil {
			return false, err
		}
		if volume.StoreID != r.Reference.StoreID || volume.VolumeID == "" || volume.Capacity != plan.CapacityBytes {
			return false, ErrConflict
		}
		err = l.Store.write(ctx, func(tx *sql.Tx) error {
			if err := changed(tx.ExecContext(ctx, `UPDATE project_volume_slots SET volume_id=?,state='initializing',error='' WHERE project_id=? AND slot_key=? AND state='creating'`, volume.VolumeID, r.ProjectID, key)); err != nil {
				return err
			}
			return changed(tx.ExecContext(ctx, `UPDATE storage_reservations SET state='allocated' WHERE project_id=? AND slot_key=? AND state='reserved'`, r.ProjectID, key))
		})
		if err != nil {
			return false, err
		}
		slot.VolumeID = volume.VolumeID
		slot.State = "initializing"
	}
	if slot.State == "initializing" {
		if plan.Source != nil {
			backend, ok := l.Backend.(content.VolumeVerifier)
			if !ok {
				return false, Admission("artifact_unavailable", ErrInvalid)
			}
			if err := content.VerifyVolume(ctx, backend, sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: slot.VolumeID}, plan.Source.Reference.Digest); err != nil {
				return false, err
			}
		}
		err := l.Store.write(ctx, func(tx *sql.Tx) error {
			return changed(tx.ExecContext(ctx, `UPDATE project_volume_slots SET state='ready',error='' WHERE project_id=? AND slot_key=? AND state='initializing'`, r.ProjectID, key))
		})
		return err == nil, err
	}
	return false, ErrConflict
}
func (l *Lifecycle) deleteSlot(ctx context.Context, r Resources, slot VolumeSlot) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, l.Store.recordSlotFailure(ctx, r.ProjectID, slot.Key, resultErr))
		}
	}()
	var plan VolumePlan
	for _, candidate := range r.Profile.Volumes {
		if candidate.Key == slot.Key {
			plan = candidate
			break
		}
	}
	if plan.Key == "" {
		return ErrConflict
	}
	// A creating slot may have a remote allocation even when its reply was lost.
	// Resolve its original idempotent create before deleting; never free it early.
	if plan.Storage == "owned" && slot.State == "creating" {
		var volume sandbox.Volume
		var err error
		if plan.Source == nil {
			volume, err = l.Backend.CreateVolume(ctx, slot.IntentKey, plan.CapacityBytes)
		} else {
			backend, ok := l.Backend.(sandbox.VolumeDerivation)
			if !ok {
				return ErrInvalid
			}
			volume, err = backend.SnapshotVolume(ctx, sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: plan.Source.VolumeID}, slot.IntentKey, plan.CapacityBytes)
		}
		if err != nil {
			if sandbox.Is(err, sandbox.NotFound) && volume.StoreID == r.Reference.StoreID && volume.VolumeID != "" {
				return l.Store.retireVolumeSlot(ctx, r.ProjectID, slot.Key)
			}
			return err
		}
		if volume.StoreID != r.Reference.StoreID || volume.VolumeID == "" || volume.Capacity != plan.CapacityBytes {
			return ErrConflict
		}
		if err = l.Store.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE project_volume_slots SET volume_id=?,state='initializing' WHERE project_id=? AND slot_key=? AND state='creating'`, volume.VolumeID, r.ProjectID, slot.Key); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE storage_reservations SET state='allocated' WHERE project_id=? AND slot_key=? AND state='reserved'`, r.ProjectID, slot.Key)
			return err
		}); err != nil {
			return err
		}
		slot.VolumeID = volume.VolumeID
	}
	if plan.Storage == "owned" && slot.VolumeID != "" {
		if err := l.Store.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE project_volume_slots SET state='deleting' WHERE project_id=? AND slot_key=? AND state<>'deleted'`, r.ProjectID, slot.Key)
			return err
		}); err != nil {
			return err
		}
		if err := l.Backend.DeleteVolume(ctx, sandbox.VolumeReference{StoreID: r.Reference.StoreID, VolumeID: slot.VolumeID}); err != nil && !sandbox.Is(err, sandbox.NotFound) {
			return err
		}
	}
	return l.Store.retireVolumeSlot(ctx, r.ProjectID, slot.Key)
}
func (s *Store) retireVolumeSlot(ctx context.Context, project, key string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE project_volume_slots SET state='deleted',error='' WHERE project_id=? AND slot_key=?`, project, key); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE storage_reservations SET state='released' WHERE project_id=? AND slot_key=?`, project, key)
		return err
	})
}

type StorageUsage struct {
	Reserved  int64 `json:"reserved"`
	Allocated int64 `json:"allocated"`
}

func storageUsage(ctx context.Context, q queryer, account string) (StorageUsage, error) {
	var out StorageUsage
	rows, err := q.QueryContext(ctx, `SELECT r.capacity,r.state FROM storage_reservations r JOIN projects p ON p.id=r.project_id WHERE p.owner_id=? AND r.state<>'released'`, account)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var capacity int64
		var state string
		if err = rows.Scan(&capacity, &state); err != nil {
			return out, err
		}
		if state == "reserved" {
			out.Reserved, err = checkedSum(out.Reserved, capacity)
		} else {
			out.Allocated, err = checkedSum(out.Allocated, capacity)
		}
		if err != nil {
			return out, err
		}
	}
	return out, rows.Err()
}

func validateVolumePlan(plans []VolumePlan) error {
	if len(plans) < 2 || len(plans) > 128 {
		return ErrInvalid
	}
	keys, roles := map[string]bool{}, map[string]int{}
	var total int64
	for index, plan := range plans {
		if !validID(plan.Key) || len(plan.Key) > 64 || keys[plan.Key] || !validMountPath(plan.Destination) || plan.CapacityBytes <= 0 {
			return ErrInvalid
		}
		for _, root := range runtimerelease.DefaultMountContract().ProtectedRoots {
			if pathsOverlap(plan.Destination, root) {
				return ErrInvalid
			}
		}
		if !validID(plan.Role) || len(plan.Role) > 64 {
			return ErrInvalid
		}

		keys[plan.Key] = true
		roles[plan.Role]++
		for _, other := range plans[:index] {
			if pathsOverlap(plan.Destination, other.Destination) {
				return ErrInvalid
			}
		}

		if plan.Role != "data" && roles[plan.Role] > 1 {
			return ErrInvalid
		}
		if plan.Access != "read_write" && plan.Access != "read_only" {
			return ErrInvalid
		}
		if plan.Storage != "owned" && plan.Storage != "shared_readonly" {
			return ErrInvalid
		}
		if plan.Role == "workspace" || plan.Role == "runtime" {

			if plan.Storage != "owned" || plan.Access != "read_write" {
				return ErrInvalid
			}
		}
		if plan.Storage == "shared_readonly" && (plan.Source == nil || plan.Access != "read_only") {
			return ErrInvalid
		}
		if plan.Source != nil {
			if err := plan.Source.Reference.validate(); err != nil {
				return err
			}
			if plan.Source.VolumeID == "" || plan.Source.MinimumBytes <= 0 || plan.CapacityBytes < plan.Source.MinimumBytes {
				return ErrInvalid
			}
			if plan.Role == "runtime" && !plan.Source.CleanRuntime {
				return ErrInvalid
			}
		}
		if plan.Storage == "owned" {
			var err error
			total, err = checkedSum(total, plan.CapacityBytes)
			if err != nil {
				return err
			}
		}
	}
	if roles["workspace"] != 1 || roles["runtime"] != 1 {
		return ErrInvalid
	}
	return nil
}

// Slot diagnostics expose a bounded product/mechanism code, never remote host
// paths or transport credentials. The operation still returns its original error.
func (s *Store) recordSlotFailure(ctx context.Context, project, key string, cause error) error {
	code := "volume_operation_unconfirmed"
	var admission *AdmissionError
	var mechanism *sandbox.Error
	var quota *QuotaError
	if errors.As(cause, &quota) {
		code = "quota_exceeded:" + string(quota.Dimension)
	} else if errors.As(cause, &admission) {
		code = admission.Code
	} else if errors.As(cause, &mechanism) {
		code = "volume_" + string(mechanism.Class)
	} else if errors.Is(cause, content.ErrContentRefused) {
		code = "seed_verification_failed"
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return s.write(settle, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(settle, `UPDATE project_volume_slots SET error=? WHERE project_id=? AND slot_key=? AND state NOT IN ('ready','deleted')`, code, project, key)
		return err
	})
}
