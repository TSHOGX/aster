package product

import (
	"context"
	"database/sql"
	"encoding/json"
)

type SavedPlanConflict struct {
	ProjectID string             `json:"project_id"`
	State     string             `json:"state"`
	Issues    []ResourceFitIssue `json:"issues"`
}
type SavedPlanConflicts struct {
	Count     int64               `json:"count,string"`
	Projects  []SavedPlanConflict `json:"projects"`
	Truncated bool                `json:"truncated"`
}
type UserUsage struct {
	Settings          UserResourceSettings                `json:"settings"`
	Usage             map[ResourceDimension]ResourceUsage `json:"usage"`
	Overages          []ResourceFitIssue                  `json:"overages"`
	DefaultPlanIssues []ResourceFitIssue                  `json:"default_plan_issues"`
	SavedPlans        SavedPlanConflicts                  `json:"saved_plan_conflicts"`
	Network           UserNetworkSettings                 `json:"network"`
}

func accountResourceUsage(ctx context.Context, q queryer, actor string) (map[ResourceDimension]ResourceUsage, error) {
	out, err := computeUsage(ctx, q, actor)
	if err != nil {
		return nil, err
	}
	for _, dimension := range []ResourceDimension{ResourceProjects, ResourceAgents, ResourceTerminals, ResourceSchedules} {
		out[dimension], err = scalarUsage(ctx, q, actor, dimension)
		if err != nil {
			return nil, err
		}
	}
	storage, err := storageUsage(ctx, q, actor)
	if err != nil {
		return nil, err
	}
	out[ResourcePersistent] = ResourceUsage{Used: storage.Allocated, Reserved: storage.Reserved}
	return out, nil
}
func resourceOverages(usage map[ResourceDimension]ResourceUsage, values map[ResourceDimension]EffectiveResource) ([]ResourceFitIssue, error) {
	out := []ResourceFitIssue{}
	for _, dimension := range resourceDimensions {
		amount, err := checkedSum(usage[dimension].Used, usage[dimension].Reserved)
		if err != nil {
			return nil, err
		}
		if amount > values[dimension].Effective {
			out = append(out, ResourceFitIssue{Code: "quota_exceeded", Dimension: dimension, Requested: amount, Limit: values[dimension].Effective})
		}
	}
	return out, nil
}

// Saved plans are immutable. Report the exact dimensions that would block new
// provisioning/resume; existing data and admitted work are not resized or stopped.
func savedPlanResourceFit(profile Profile, values map[ResourceDimension]EffectiveResource, compute bool) ([]ResourceFitIssue, error) {
	issues := []ResourceFitIssue{}
	check := func(d ResourceDimension, amount int64, field string) {
		if amount > values[d].Effective {
			issues = append(issues, ResourceFitIssue{Code: "quota_exceeded", Dimension: d, Requested: amount, Limit: values[d].Effective, Field: field})
		}
	}
	var owned int64
	for _, slot := range profile.Volumes {
		if slot.Storage != "owned" {
			continue
		}
		var err error
		owned, err = checkedSum(owned, slot.CapacityBytes)
		if err != nil {
			return nil, err
		}
		check(ResourceVolume, slot.CapacityBytes, "mounts."+slot.Key+".capacity")
	}
	check(ResourceWorkspace, profile.CapacityForRole("workspace"), "workspace_bytes")
	check(ResourceRuntime, profile.CapacityForRole("runtime"), "runtime_bytes")
	check(ResourceProjectVolumes, owned, "owned_bytes")
	check(ResourceMounts, int64(len(profile.Volumes)), "mounts")
	if compute {
		check(ResourceCPU, profile.CPUMillis, "cpu_millis")
		check(ResourceMemory, profile.MemoryBytes, "memory_bytes")
		check(ResourceOverlay, profile.DiskBytes, "overlay_bytes")
		// A single retained Project must fit even when all sibling compute stops.
		check(ResourceAggregateCPU, profile.CPUMillis, "cpu_millis")
		check(ResourceAggregateMemory, profile.MemoryBytes, "memory_bytes")
	}
	return issues, nil
}
func savedPlanConflicts(ctx context.Context, q queryer, actor string, values map[ResourceDimension]EffectiveResource) (SavedPlanConflicts, error) {
	out := SavedPlanConflicts{Projects: []SavedPlanConflict{}}
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.state,r.profile FROM projects p JOIN project_resources r ON r.project_id=p.id WHERE p.owner_id=? AND p.state<>'deleted' ORDER BY p.id`, actor)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item SavedPlanConflict
		var raw string
		if err = rows.Scan(&item.ProjectID, &item.State, &raw); err != nil {
			return out, err
		}
		var profile Profile
		if err = json.Unmarshal([]byte(raw), &profile); err != nil {
			return out, err
		}
		item.Issues, err = savedPlanResourceFit(profile, values, true)
		if err != nil {
			return out, err
		}
		if len(item.Issues) == 0 {
			continue
		}
		out.Count++
		if len(out.Projects) < 20 {
			out.Projects = append(out.Projects, item)
		} else {
			out.Truncated = true
		}
	}
	return out, rows.Err()
}
func userUsage(ctx context.Context, q queryer, actor string) (out UserUsage, err error) {
	out.Settings, err = userResourceSettings(ctx, q, actor)
	if err != nil {
		return
	}
	out.Usage, err = accountResourceUsage(ctx, q, actor)
	if err != nil {
		return
	}
	out.Overages, err = resourceOverages(out.Usage, out.Settings.Effective)
	if err != nil {
		return
	}
	out.SavedPlans, err = savedPlanConflicts(ctx, q, actor, out.Settings.Effective)
	if err != nil {
		return
	}
	environment, artifacts, err := currentEnvironment(ctx, q)
	if err != nil {
		return out, err
	}
	release, err := registeredSandbox(ctx, q, environment.SandboxRelease, false)
	if err != nil {
		return out, err
	}
	_, out.DefaultPlanIssues, err = EnvironmentResourceFit(environment, release.Manifest.Mounts, artifacts, out.Settings.Effective, out.Settings.Effective[ResourceMounts].PlatformLimit)
	if err != nil {
		return
	}
	out.Network, err = userNetworkSettings(ctx, q, actor)
	return
}
func (s *Store) AdminUserUsage(ctx context.Context, token, actor string) (UserUsage, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UserUsage{}, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return UserUsage{}, err
	}
	out, err := userUsage(ctx, tx, actor)
	if err != nil {
		return UserUsage{}, err
	}
	return out, tx.Commit()
}
func (s *Store) OwnUsage(ctx context.Context, token string) (UserUsage, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UserUsage{}, err
	}
	defer tx.Rollback()
	session, err := s.normalLogin(ctx, tx, token)
	if err != nil {
		return UserUsage{}, err
	}
	out, err := userUsage(ctx, tx, session.Account.ID)
	if err != nil {
		return UserUsage{}, err
	}
	return out, tx.Commit()
}
