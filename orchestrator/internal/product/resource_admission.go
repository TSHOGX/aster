package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrQuotaExceeded = errors.New("resource allowance exceeded")

type QuotaError struct {
	Dimension      ResourceDimension `json:"dimension"`
	Used           int64             `json:"used,string"`
	Reserved       int64             `json:"reserved,string"`
	RequestedDelta int64             `json:"requested_delta,string"`
	Limit          int64             `json:"limit,string"`
}

func (e *QuotaError) Error() string { return fmt.Sprintf("quota_exceeded: %s", e.Dimension) }
func (e *QuotaError) Unwrap() error { return ErrQuotaExceeded }

type ResourceUsage struct {
	Used     int64 `json:"used,string"`
	Reserved int64 `json:"reserved,string"`
}

func quotaValues(ctx context.Context, q queryer, actor string) (map[ResourceDimension]EffectiveResource, error) {
	if err := activeAccount(ctx, q, actor); err != nil {
		return nil, err
	}
	settings, err := userResourceSettings(ctx, q, actor)
	return settings.Effective, err
}
func quotaCheck(dimension ResourceDimension, usage ResourceUsage, delta, limit int64) error {
	if delta < 0 || limit < 0 {
		return ErrInvalid
	}
	total, err := checkedSum(usage.Used, usage.Reserved)
	if err != nil {
		return err
	}
	total, err = checkedSum(total, delta)
	if err != nil {
		return err
	}
	if total > limit {
		return &QuotaError{Dimension: dimension, Used: usage.Used, Reserved: usage.Reserved, RequestedDelta: delta, Limit: limit}
	}
	return nil
}
func scalarUsage(ctx context.Context, q queryer, actor string, dimension ResourceDimension) (ResourceUsage, error) {
	var sql string
	switch dimension {
	case ResourceProjects:
		sql = `SELECT count(*),0 FROM projects WHERE owner_id=? AND state<>'deleted'`
	case ResourceAgents:
		sql = `SELECT coalesce(sum(i.state='running'),0),coalesce(sum(i.state<>'running'),0) FROM agent_incarnations i JOIN projects p ON p.id=i.project_id WHERE p.owner_id=? AND i.state<>'exited'`
	case ResourceTerminals:
		sql = `SELECT coalesce(sum(i.state='running'),0),coalesce(sum(i.state<>'running'),0) FROM terminal_incarnations i JOIN projects p ON p.id=i.project_id WHERE p.owner_id=? AND i.state IN ('starting','running')`
	case ResourceSchedules:
		sql = `SELECT count(*),0 FROM schedules s JOIN projects p ON p.id=s.project_id WHERE p.owner_id=? AND s.state='active'`
	default:
		return ResourceUsage{}, ErrInvalid
	}
	var usage ResourceUsage
	err := q.QueryRowContext(ctx, sql, actor).Scan(&usage.Used, &usage.Reserved)
	return usage, err
}
func admitCount(ctx context.Context, q queryer, actor string, dimension ResourceDimension, delta int64) error {
	values, err := quotaValues(ctx, q, actor)
	if err != nil {
		return err
	}
	usage, err := scalarUsage(ctx, q, actor, dimension)
	if err != nil {
		return err
	}
	return quotaCheck(dimension, usage, delta, values[dimension].Effective)
}
func computeUsage(ctx context.Context, q queryer, actor string) (map[ResourceDimension]ResourceUsage, error) {
	out := map[ResourceDimension]ResourceUsage{ResourceRunningProjects: {}, ResourceAggregateCPU: {}, ResourceAggregateMemory: {}}
	rows, err := q.QueryContext(ctx, `SELECT r.profile,p.state,r.sandbox_id FROM project_resources r JOIN projects p ON p.id=r.project_id WHERE p.owner_id=? AND r.occupied=1`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, state, sandboxID string
		if err = rows.Scan(&raw, &state, &sandboxID); err != nil {
			return nil, err
		}
		var profile Profile
		if err = json.Unmarshal([]byte(raw), &profile); err != nil {
			return nil, err
		}
		reserved := state == "provisioning" || state == "resuming" || sandboxID == ""
		for dimension, amount := range map[ResourceDimension]int64{ResourceRunningProjects: 1, ResourceAggregateCPU: profile.CPUMillis, ResourceAggregateMemory: profile.MemoryBytes} {
			value := out[dimension]
			if reserved {
				value.Reserved, err = checkedSum(value.Reserved, amount)
			} else {
				value.Used, err = checkedSum(value.Used, amount)
			}
			if err != nil {
				return nil, err
			}
			out[dimension] = value
		}
	}
	return out, rows.Err()
}
func planQuota(profile Profile, values map[ResourceDimension]EffectiveResource) error {
	issues, err := savedPlanResourceFit(profile, values, false)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		issue := issues[0]
		return &QuotaError{Dimension: issue.Dimension, RequestedDelta: issue.Requested, Limit: issue.Limit}
	}
	return nil
}

func admitStoragePlan(ctx context.Context, q queryer, actor string, profile Profile) error {
	values, err := quotaValues(ctx, q, actor)
	if err != nil {
		return err
	}
	if err = planQuota(profile, values); err != nil {
		return err
	}
	var bytes int64
	for _, slot := range profile.Volumes {
		if slot.Storage == "owned" {
			bytes, err = checkedSum(bytes, slot.CapacityBytes)
			if err != nil {
				return err
			}
		}
	}
	usage, err := storageUsage(ctx, q, actor)
	if err != nil {
		return err
	}
	return quotaCheck(ResourcePersistent, ResourceUsage{Used: usage.Allocated, Reserved: usage.Reserved}, bytes, values[ResourcePersistent].Effective)
}
func admitCompute(ctx context.Context, q queryer, actor string, profile Profile, alreadyReserved bool) error {
	values, err := quotaValues(ctx, q, actor)
	if err != nil {
		return err
	}
	if err = planQuota(profile, values); err != nil {
		return err
	}
	for _, entry := range []struct {
		dimension ResourceDimension
		amount    int64
	}{{ResourceCPU, profile.CPUMillis}, {ResourceMemory, profile.MemoryBytes}, {ResourceOverlay, profile.DiskBytes}} {
		if err = quotaCheck(entry.dimension, ResourceUsage{}, entry.amount, values[entry.dimension].Effective); err != nil {
			return err
		}
	}
	storage, err := storageUsage(ctx, q, actor)
	if err != nil {
		return err
	}
	if err = quotaCheck(ResourcePersistent, ResourceUsage{Used: storage.Allocated, Reserved: storage.Reserved}, 0, values[ResourcePersistent].Effective); err != nil {
		return err
	}
	usage, err := computeUsage(ctx, q, actor)
	if err != nil {
		return err
	}
	for _, entry := range []struct {
		dimension ResourceDimension
		amount    int64
	}{{ResourceRunningProjects, 1}, {ResourceAggregateCPU, profile.CPUMillis}, {ResourceAggregateMemory, profile.MemoryBytes}} {
		delta := entry.amount
		if alreadyReserved {
			delta = 0
		}
		if err = quotaCheck(entry.dimension, usage[entry.dimension], delta, values[entry.dimension].Effective); err != nil {
			return err
		}
	}
	return nil
}
func admitReservedStorage(ctx context.Context, q queryer, actor string, profile Profile) error {
	values, err := quotaValues(ctx, q, actor)
	if err != nil {
		return err
	}
	if err = planQuota(profile, values); err != nil {
		return err
	}
	usage, err := storageUsage(ctx, q, actor)
	if err != nil {
		return err
	}
	return quotaCheck(ResourcePersistent, ResourceUsage{Used: usage.Allocated, Reserved: usage.Reserved}, 0, values[ResourcePersistent].Effective)
}
func transferLimit(ctx context.Context, q queryer, actor string) (int64, error) {
	values, err := quotaValues(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	return values[ResourceTransfer].Effective, nil
}

func admitTransfer(ctx context.Context, q queryer, actor string, bytes int64) error {
	limit, err := transferLimit(ctx, q, actor)
	if err != nil {
		return err
	}
	return quotaCheck(ResourceTransfer, ResourceUsage{}, bytes, limit)
}
