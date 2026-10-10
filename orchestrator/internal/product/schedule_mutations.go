package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

// SchedulePatch exposes only authored definition fields. Operational state and
// attribution cannot be supplied by a browser or model.
type SchedulePatch struct {
	SessionID      *string `json:"session_id,omitempty"`
	Title          *string `json:"title,omitempty"`
	Prompt         *string `json:"prompt,omitempty"`
	TriggerKind    *string `json:"trigger_kind,omitempty"`
	Cron           *string `json:"cron,omitempty"`
	Timezone       *string `json:"timezone,omitempty"`
	RunAt          *string `json:"run_at,omitempty"`
	ExpiresAt      *string `json:"expires_at,omitempty"`
	State          *string `json:"state,omitempty"`
	Notify         *bool   `json:"notify,omitempty"`
	MaxOccurrences *int    `json:"max_occurrences,omitempty"`
	StartsAt       *string `json:"starts_at,omitempty"`
	NotifyPolicy   *string `json:"notification_policy,omitempty"`
}
type ScheduleMutation struct {
	RequestID string        `json:"request_id"`
	Operation string        `json:"operation"`
	ID        string        `json:"id"`
	Revision  int64         `json:"revision"`
	Patch     SchedulePatch `json:"patch"`
}
type ScheduleOrigin struct{ SessionID, RunID, ToolCallID string }
type ScheduleReceipt struct {
	Kind          string   `json:"kind"`
	Version       int      `json:"version"`
	RequestID     string   `json:"request_id"`
	ReferenceTime string   `json:"reference_time"`
	Schedule      Schedule `json:"schedule"`
	Admitted      bool     `json:"admitted"`
}

func (p SchedulePatch) apply(s Schedule) Schedule {
	for _, v := range []struct {
		dst *string
		src *string
	}{
		{&s.SessionID, p.SessionID}, {&s.Title, p.Title}, {&s.Prompt, p.Prompt},
		{&s.TriggerKind, p.TriggerKind}, {&s.Cron, p.Cron}, {&s.Timezone, p.Timezone},
		{&s.RunAt, p.RunAt}, {&s.ExpiresAt, p.ExpiresAt}, {&s.State, p.State},
	} {
		if v.src != nil {
			*v.dst = *v.src
		}
	}
	if p.Notify != nil {
		s.Notify = *p.Notify
	}
	if p.MaxOccurrences != nil {
		s.MaxOccurrences = p.MaxOccurrences
		if *p.MaxOccurrences == 0 {
			s.MaxOccurrences = nil
		}
	}
	if p.StartsAt != nil {
		s.StartsAt = *p.StartsAt
	}
	if p.NotifyPolicy != nil {
		s.NotifyPolicy = *p.NotifyPolicy
	}
	return s
}
func (s Schedule) definitionPatch() SchedulePatch {
	return SchedulePatch{SessionID: &s.SessionID, Title: &s.Title, Prompt: &s.Prompt, TriggerKind: &s.TriggerKind, Cron: &s.Cron, Timezone: &s.Timezone, RunAt: &s.RunAt, ExpiresAt: &s.ExpiresAt, State: &s.State, Notify: &s.Notify, MaxOccurrences: s.MaxOccurrences, StartsAt: &s.StartsAt, NotifyPolicy: &s.NotifyPolicy}
}
func (s *Store) MutateSchedule(ctx context.Context, scope Scope, input ScheduleMutation) (ScheduleReceipt, error) {
	var out ScheduleReceipt
	err := s.write(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = s.mutateSchedule(ctx, tx, scope, input, ScheduleOrigin{})
		return e
	})
	return out, err
}

// mutateSchedule is also called by the owner platform adapter after checking
// live gateway authority inside this same transaction.
func (s *Store) mutateSchedule(ctx context.Context, tx *sql.Tx, scope Scope, input ScheduleMutation, origin ScheduleOrigin) (ScheduleReceipt, error) {
	var out ScheduleReceipt
	if _, err := writableProject(ctx, tx, scope); err != nil {
		return out, err
	}
	if !validText(input.RequestID, 128) || !validID(input.ID) {
		return out, ErrInvalid
	}
	if input.Operation != "create" && input.Operation != "update" && input.Operation != "cancel" && input.Operation != "delete" && input.Operation != "finish" {
		return out, ErrInvalid
	}
	if (input.Operation == "cancel" || input.Operation == "delete" || input.Operation == "finish") && input.Patch != (SchedulePatch{}) {
		return out, ErrInvalid
	}
	for _, field := range []**string{&input.Patch.RunAt, &input.Patch.ExpiresAt, &input.Patch.StartsAt} {
		if *field != nil && **field != "" {
			instant, e := time.Parse(time.RFC3339Nano, **field)
			if e != nil {
				return out, ErrInvalid
			}
			canonical := sqlitex.FormatTime(instant)
			*field = &canonical
		}
	}
	digest := creationDigest(input, origin)
	var stored, result string
	err := tx.QueryRowContext(ctx, `SELECT digest,result FROM schedule_mutation_receipts WHERE project_id=? AND request_id=?`, scope.ProjectID, input.RequestID).Scan(&stored, &result)
	if err == nil {
		if stored != digest {
			return out, ErrConflict
		}
		if result == "{}" {
			return out, ErrNotFound
		} // Retired private receipt; identity remains reserved.
		err = json.Unmarshal([]byte(result), &out)
		return out, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	release, err := releaseState(ctx, tx)
	if err != nil {
		return out, err
	}
	if release.Phase != "open" {
		return out, ErrDraining
	}
	switch input.Operation {
	case "create":
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedules WHERE id=?)`, input.ID).Scan(&exists); err != nil {
			return out, err
		}
		if exists {
			return out, ErrConflict
		}
		task := input.Patch.apply(Schedule{ID: input.ID, State: "active", Notify: true})
		task.OriginSessionID, task.OriginRunID, task.OriginToolCallID = origin.SessionID, origin.RunID, origin.ToolCallID
		_, err = s.createSchedule(ctx, tx, scope, task)
	case "update", "delete":
		var task Schedule
		task, err = scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, input.ID, scope.ProjectID))
		if err == nil {
			task = input.Patch.apply(task)
			task.Revision = input.Revision
			err = s.updateSchedule(ctx, tx, scope, task, input.Operation == "delete")
		}
	case "finish":
		err = s.finishSchedule(ctx, tx, scope, input, origin)
	case "cancel":
		err = s.cancelSchedule(ctx, tx, scope, input.ID, input.Revision)
	}
	if err != nil {
		return out, err
	}
	out = ScheduleReceipt{Kind: "schedule_receipt", Version: 1, RequestID: input.RequestID, ReferenceTime: s.timestamp()}
	out.Schedule, err = scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=?`, input.ID))
	if err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_runs WHERE schedule_id=?)`, input.ID).Scan(&out.Admitted); err != nil {
		return out, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_mutation_receipts(project_id,request_id,digest,schedule_id,result,origin_session_id,origin_run_id,created_at) VALUES(?,?,?,?,?,nullif(?,''),nullif(?,''),?)`, scope.ProjectID, input.RequestID, digest, input.ID, string(raw), origin.SessionID, origin.RunID, s.timestamp())
	return out, err
}
