package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"harness-platform/orchestrator/internal/sandbox"
)

type AdminProjectQuery struct {
	OwnerID, State, Search, Cursor string
	Limit                          int
}
type AdminProjectSummary struct {
	ExecutionHoldReason string             `json:"execution_hold_reason,omitempty"`
	ExecutionSuspended  bool               `json:"execution_suspended"`
	ID                  string             `json:"id"`
	Title               string             `json:"title"`
	OwnerID             string             `json:"owner_id"`
	OwnerUsername       string             `json:"owner_username"`
	OwnerStatus         string             `json:"owner_status"`
	State               string             `json:"state"`
	Revision            int64              `json:"revision"`
	UpdatedAt           string             `json:"updated_at"`
	Bound               bool               `json:"bound"`
	EnvironmentRevision int64              `json:"environment_revision"`
	SandboxRelease      *ArtifactReference `json:"sandbox_release"`
	AllocatedBytes      int64              `json:"allocated_bytes,string"`
	ReservedBytes       int64              `json:"reserved_bytes,string"`
	SlotCount           int64              `json:"slot_count,string"`
}
type AdminProjectPage struct {
	Projects   []AdminProjectSummary `json:"projects"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

const adminProjectSelect = `SELECT p.id,p.title,p.owner_id,a.username,a.status,p.state,p.revision,p.updated_at,r.project_id IS NOT NULL,coalesce(json_extract(r.profile,'$.environment_revision'),0),coalesce(json_extract(r.profile,'$.sandbox_release.id'),''),coalesce(json_extract(r.profile,'$.sandbox_release.version'),''),coalesce(json_extract(r.profile,'$.sandbox_release.digest'),''),coalesce((SELECT sum(capacity) FROM storage_reservations s WHERE s.project_id=p.id AND s.state='allocated'),0),coalesce((SELECT sum(capacity) FROM storage_reservations s WHERE s.project_id=p.id AND s.state='reserved'),0),coalesce(json_array_length(r.profile,'$.volumes'),0),p.execution_suspended,p.execution_hold_reason FROM projects p JOIN accounts a ON a.id=p.owner_id LEFT JOIN project_resources r ON r.project_id=p.id `

func scanAdminProject(row scanner) (out AdminProjectSummary, err error) {
	var release ArtifactReference
	err = missing(row.Scan(&out.ID, &out.Title, &out.OwnerID, &out.OwnerUsername, &out.OwnerStatus, &out.State, &out.Revision, &out.UpdatedAt, &out.Bound, &out.EnvironmentRevision, &release.ID, &release.Version, &release.Digest, &out.AllocatedBytes, &out.ReservedBytes, &out.SlotCount, &out.ExecutionSuspended, &out.ExecutionHoldReason))
	if out.Bound {
		out.SandboxRelease = &release
	}
	return
}
func (s *Store) AdminProjects(ctx context.Context, token string, filter AdminProjectQuery) (AdminProjectPage, error) {
	out := AdminProjectPage{Projects: []AdminProjectSummary{}}
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return out, err
	}
	limit, err := adminLimit(filter.Limit)
	if err != nil {
		return out, err
	}
	states := map[string]bool{"": true, "created": true, "provisioning": true, "active": true, "pausing": true, "paused": true, "resuming": true, "blocked": true, "deleting": true, "deleted": true}
	if !states[filter.State] || filter.OwnerID != "" && !validID(filter.OwnerID) || !utf8.ValidString(filter.Search) || len(filter.Search) > 256 || strings.ContainsRune(filter.Search, 0) {
		return out, ErrInvalid
	}
	filter.Search = strings.TrimSpace(filter.Search)
	scope := cursorScope("admin-projects", []string{filter.OwnerID, filter.State, filter.Search})
	after, err := readAdminCursor(filter.Cursor, scope)
	if err != nil {
		return out, err
	}
	rows, err := s.reader.QueryContext(ctx, adminProjectSelect+`WHERE p.id>? AND (?='' OR p.owner_id=?) AND (?='' OR p.state=?) AND (?='' OR instr(lower(p.title),lower(?))>0 OR instr(lower(p.id),lower(?))>0) ORDER BY p.id LIMIT ?`, after, filter.OwnerID, filter.OwnerID, filter.State, filter.State, filter.Search, filter.Search, filter.Search, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		project, e := scanAdminProject(rows)
		if e != nil {
			return out, e
		}
		out.Projects = append(out.Projects, project)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Projects) > limit {
		out.Projects = out.Projects[:limit]
		out.NextCursor = nextAdminCursor(out.Projects[limit-1].ID, scope)
	}
	return out, nil
}

type ProjectInspector interface {
	InspectConfiguration(context.Context, sandbox.Reference) (sandbox.Configuration, error)
	InspectVolume(context.Context, sandbox.VolumeReference) (sandbox.Volume, error)
}
type AdminVolumeSlot struct {
	CleanRuntime          bool               `json:"clean_runtime"`
	Key                   string             `json:"key"`
	Role                  string             `json:"role"`
	Destination           string             `json:"destination"`
	Storage               string             `json:"storage"`
	Access                string             `json:"access"`
	CapacityBytes         int64              `json:"capacity_bytes,string"`
	Source                *ArtifactReference `json:"source,omitempty"`
	SourceMinimumBytes    int64              `json:"source_minimum_bytes,string"`
	VolumeID              string             `json:"volume_id,omitempty"`
	State                 string             `json:"state"`
	Error                 string             `json:"error,omitempty"`
	AllocatedBytes        int64              `json:"allocated_bytes,string"`
	ReservedBytes         int64              `json:"reserved_bytes,string"`
	Observation           string             `json:"observation"`
	ObservedCapacityBytes *int64             `json:"observed_capacity_bytes,string,omitempty"`
	CheckedAt             string             `json:"checked_at,omitempty"`
}
type AdminProjectBinding struct {
	ResourceRevision int64             `json:"resource_revision"`
	StoreID          string            `json:"store_id"`
	TemplateID       string            `json:"template_id"`
	SandboxID        string            `json:"sandbox_id,omitempty"`
	Epoch            uint64            `json:"epoch,string"`
	Occupied         bool              `json:"occupied"`
	CPUMillis        int64             `json:"cpu_millis,string"`
	MemoryBytes      int64             `json:"memory_bytes,string"`
	OverlayBytes     int64             `json:"overlay_bytes,string"`
	Slots            []AdminVolumeSlot `json:"slots"`
}
type AdminLifecycleOperation struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Phase           string `json:"phase"`
	Error           string `json:"error,omitempty"`
	CreatedAt       string `json:"created_at"`
	AttemptedAt     string `json:"attempted_at"`
	NetworkRevision string `json:"network_revision,omitempty"`
}
type AdminProjectObservation struct {
	Status              string `json:"status"`
	CheckedAt           string `json:"checked_at,omitempty"`
	State               string `json:"state,omitempty"`
	PlacementState      string `json:"placement_state,omitempty"`
	MatchesSavedBinding *bool  `json:"matches_saved_binding,omitempty"`
	NetworkRevision     string `json:"network_revision,omitempty"`
}
type AdminProjectDetail struct {
	ControlPending bool `json:"control_pending"`
	AdminProjectSummary
	Failure              string                   `json:"failure,omitempty"`
	Binding              *AdminProjectBinding     `json:"binding"`
	DesiredNetwork       NetworkPolicy            `json:"desired_network"`
	NetworkMode          string                   `json:"network_mode"`
	SavedNetworkRevision string                   `json:"saved_network_revision,omitempty"`
	NetworkPending       bool                     `json:"network_pending"`
	Observation          AdminProjectObservation  `json:"observation"`
	Operation            *AdminLifecycleOperation `json:"operation"`
	AdminOperations      []string                 `json:"admin_operations"`
}

func (s *Store) AdminProject(ctx context.Context, token, id string, inspector ProjectInspector) (AdminProjectDetail, error) {
	var out AdminProjectDetail
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	out.AdminProjectSummary, err = scanAdminProject(tx.QueryRowContext(ctx, adminProjectSelect+`WHERE p.id=?`, id))
	if err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT failure FROM projects WHERE id=?`, id).Scan(&out.Failure); err != nil {
		return out, err
	}
	network, err := projectNetworkSettings(ctx, tx, out.ID)
	if err != nil {
		return out, err
	}
	out.DesiredNetwork = network.Desired
	out.NetworkMode = network.Mode
	out.Observation.Status = "unbound"
	var retained Resources
	if out.Bound {
		retained, err = resources(ctx, tx, id)
		if err != nil {
			return out, err
		}
		p := retained.Profile
		out.Binding = &AdminProjectBinding{ResourceRevision: p.ResourceRevision, StoreID: p.StoreID, TemplateID: p.TemplateID, SandboxID: retained.Reference.SandboxID, Epoch: retained.Reference.Epoch, Occupied: retained.Occupied, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, OverlayBytes: p.DiskBytes, Slots: []AdminVolumeSlot{}}
		out.SavedNetworkRevision = p.NetworkPolicyRevision
		out.NetworkPending = network.Desired.Revision != p.NetworkPolicyRevision
		out.Observation.Status = "unchecked"
		if retained.Reference.SandboxID == "" {
			out.Observation.Status = "not_materialized"
		}
		for _, plan := range p.Volumes {
			slot := AdminVolumeSlot{Key: plan.Key, Role: plan.Role, Destination: plan.Destination, Storage: plan.Storage, Access: plan.Access, CapacityBytes: plan.CapacityBytes, Observation: "not_materialized"}
			if plan.Source != nil {
				reference := plan.Source.Reference
				slot.Source = &reference
				slot.SourceMinimumBytes = plan.Source.MinimumBytes
				slot.CleanRuntime = plan.Source.CleanRuntime
			}
			for _, state := range retained.Volumes {
				if state.Key == plan.Key {
					slot.VolumeID = state.VolumeID
					slot.State = state.State
					slot.Error = state.Error
					break
				}
			}
			if slot.VolumeID != "" {
				slot.Observation = "unchecked"
			}
			if slot.Storage == "owned" {
				var state string
				var capacity int64
				if err = tx.QueryRowContext(ctx, `SELECT capacity,state FROM storage_reservations WHERE project_id=? AND slot_key=?`, id, slot.Key).Scan(&capacity, &state); err != nil {
					return out, err
				}
				if state == "reserved" {
					slot.ReservedBytes = capacity
				}
				if state == "allocated" {
					slot.AllocatedBytes = capacity
				}
			}
			if slot.State == "deleted" {
				slot.Observation = "deleted"
			}
			out.Binding.Slots = append(out.Binding.Slots, slot)
		}
	}
	var op AdminLifecycleOperation
	var request string
	err = tx.QueryRowContext(ctx, `SELECT id,kind,state,phase,error,created_at,attempted_at,request FROM project_operations WHERE project_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, id).Scan(&op.ID, &op.Kind, &op.State, &op.Phase, &op.Error, &op.CreatedAt, &op.AttemptedAt, &request)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		var intent operationRequest
		if err = json.Unmarshal([]byte(request), &intent); err != nil {
			return out, err
		}
		if intent.NetworkPolicy != nil {
			op.NetworkRevision = intent.NetworkPolicy.Revision
			if op.State != "succeeded" {
				out.NetworkPending = true
			}
		}
		out.Operation = &op
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_operation_targets t JOIN admin_operations o ON o.id=t.operation_id WHERE t.project_id=? AND o.state NOT IN ('succeeded','failed'))`, id).Scan(&out.ControlPending); err != nil {
		return out, err
	}
	out.AdminOperations = []string{}
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM admin_operation_targets WHERE project_id=? ORDER BY rowid DESC LIMIT 20`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var operation string
		if err = rows.Scan(&operation); err != nil {
			break
		}
		out.AdminOperations = append(out.AdminOperations, operation)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	if out.State == "deleted" {
		out.Observation.Status = "deleted"
	}
	if inspector != nil && out.Binding != nil && out.State != "deleted" {
		observed, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if retained.Reference.SandboxID != "" {
			actual, e := inspector.InspectConfiguration(observed, retained.Reference)
			out.Observation.CheckedAt = s.timestamp()
			out.Observation.Status = observationStatus(e)
			if e == nil {
				matches := actual.Reference == retained.Reference && profileConfiguration(actual, retained.Profile, retained)
				out.Observation.MatchesSavedBinding = &matches
				out.Observation.State = actual.State
				out.Observation.PlacementState = actual.PlacementState
				// A running old policy is still the observed policy while a new desired
				// revision waits for explicit resume. A pending lifecycle is not readiness.
				if matches && out.State == "active" && actual.State == "running" && (out.Operation == nil || out.Operation.State == "succeeded") {
					out.Observation.NetworkRevision = out.SavedNetworkRevision
				}
			}
		}
		for index := range out.Binding.Slots {
			slot := &out.Binding.Slots[index]
			if slot.VolumeID == "" || slot.State == "deleted" || observed.Err() != nil {
				continue
			}
			volume, e := inspector.InspectVolume(observed, sandbox.VolumeReference{StoreID: retained.Reference.StoreID, VolumeID: slot.VolumeID})
			slot.Observation = observationStatus(e)
			slot.CheckedAt = s.timestamp()
			if e == nil {
				capacity := volume.Capacity
				slot.ObservedCapacityBytes = &capacity
				if capacity != slot.CapacityBytes {
					slot.Observation = "capacity_mismatch"
				}
			}
		}
	}
	if _, err = s.adminSession(ctx, s.reader, token); err != nil {
		return AdminProjectDetail{}, err
	}
	return out, nil
}
func observationStatus(err error) string {
	if err == nil {
		return "observed"
	}
	if sandbox.Is(err, sandbox.NotFound) {
		return "absent"
	}
	if sandbox.Is(err, sandbox.IdentityChanged) {
		return "store_changed"
	}
	return "unavailable"
}
