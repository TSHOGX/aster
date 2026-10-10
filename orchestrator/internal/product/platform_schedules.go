package product

import (
	"context"
	"database/sql"
	"time"
)

const PlatformSchedulesVersion = 1
const PlatformScheduleBodyLimit = 2 << 20

type ScheduleTarget struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
}

// PlatformScheduleRequest is a closed product protocol, never a Store method
// registry. The runtime injects identities; the owner derives all attribution.
type PlatformScheduleRequest struct {
	Action        string          `json:"action"`
	Operation     string          `json:"operation,omitempty"`
	ExpectedRunID string          `json:"expected_run_id,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
	ID            string          `json:"id,omitempty"`
	Revision      int64           `json:"revision,omitempty"`
	Patch         SchedulePatch   `json:"patch,omitempty"`
	Target        *ScheduleTarget `json:"target,omitempty"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	Timezone      string          `json:"timezone,omitempty"`
	After         string          `json:"after,omitempty"`
	Limit         int             `json:"limit,omitempty"`
	HistoryLimit  int             `json:"history_limit,omitempty"`
}
type PlatformScheduleContext struct {
	ReferenceTime string   `json:"reference_time"`
	ProjectID     string   `json:"project_id"`
	SessionID     string   `json:"session_id"`
	RunID         string   `json:"run_id"`
	Timezone      string   `json:"timezone,omitempty"`
	Operations    []string `json:"operations"`
}
type PlatformScheduleResult struct {
	Context   *PlatformScheduleContext `json:"context,omitempty"`
	Receipt   *ScheduleReceipt         `json:"receipt,omitempty"`
	Schedule  *Schedule                `json:"schedule,omitempty"`
	Runs      []ScheduleRun            `json:"runs,omitempty"`
	Schedules []ScheduleOverview       `json:"schedules,omitempty"`
	Next      string                   `json:"next,omitempty"`
}

// platformScheduleAuthority rechecks the original, still-open gateway slot and
// its live grant under the same writer lock as any mutation. Stream-drain
// authority intentionally permits stopping; new platform work never does.
func platformScheduleAuthority(ctx context.Context, tx *sql.Tx, slot ServiceRequest, now string) (scope Scope, a Attribution, err error) {
	if err = checkGateway(ctx, tx, slot); err != nil {
		return
	}
	err = missing(tx.QueryRowContext(ctx, `SELECT p.owner_id,g.project_id,g.id,g.session_id,g.incarnation_id,g.audience,r.id,r.prompt_id,r.state
 FROM service_request_slots q
 JOIN service_grants g ON g.id=q.grant_id AND g.audience=q.audience AND g.state='active' AND g.expires_at>?
 JOIN projects p ON p.id=g.project_id AND p.state='active'
 JOIN sessions s ON s.id=g.session_id AND s.project_id=p.id AND s.deletion_intent=0
 JOIN agent_incarnations i ON i.id=g.incarnation_id AND i.session_id=s.id AND i.project_id=p.id AND i.state='running'
 JOIN execution_runs r ON r.id=q.run_id AND r.prompt_id=q.turn_id AND r.incarnation_id=i.id AND r.owner=i.owner AND r.owner=q.run_owner AND r.session_id=s.id AND r.project_id=p.id AND r.state IN ('starting','running')
 WHERE q.audience=? AND q.slot=? AND q.sequence=? AND q.closed=0`, now, slot.Audience, slot.Slot, slot.Sequence).Scan(&scope.Actor, &scope.ProjectID, &a.Grant.ID, &a.Grant.SessionID, &a.Grant.IncarnationID, &a.Grant.Audience, &a.RunID, &a.TurnID, &a.RunState))
	a.Grant.ProjectID = scope.ProjectID
	return
}
func (s *Store) PlatformSchedules(ctx context.Context, slot ServiceRequest, input PlatformScheduleRequest) (out PlatformScheduleResult, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		scope, a, e := platformScheduleAuthority(ctx, tx, slot, s.timestamp())
		if e != nil {
			return e
		}
		if input.Action == "context" {
			if input.Timezone != "" {
				if len(input.Timezone) > 128 {
					return ErrInvalid
				}
				if _, e = time.LoadLocation(input.Timezone); e != nil {
					return ErrInvalid
				}
			}
			out.Context = &PlatformScheduleContext{ReferenceTime: s.timestamp(), ProjectID: scope.ProjectID, SessionID: a.Grant.SessionID, RunID: a.RunID, Timezone: input.Timezone, Operations: []string{"create", "list", "get", "update", "cancel"}}
			return nil
		}
		if input.Action != "execute" || input.ExpectedRunID != a.RunID {
			return ErrConflict
		}
		switch input.Operation {
		case "create", "update", "cancel":
			if !validText(input.ToolCallID, 256) {
				return ErrInvalid
			}
			if input.Target != nil {
				if input.Patch.SessionID != nil {
					return ErrInvalid
				}
				var target string
				switch input.Target.Kind {
				case "current":
					target = a.Grant.SessionID
				case "new":
				case "session":
					target = input.Target.SessionID
					if !validID(target) {
						return ErrInvalid
					}
				default:
					return ErrInvalid
				}
				input.Patch.SessionID = &target
			} else if input.Operation == "create" && input.Patch.SessionID == nil {
				input.Patch.SessionID = &a.Grant.SessionID
			}
			// Host provenance binds the execution, not a guessed user prompt segment.
			receipt, e := s.mutateSchedule(ctx, tx, scope, ScheduleMutation{RequestID: input.RequestID, Operation: input.Operation, ID: input.ID, Revision: input.Revision, Patch: input.Patch}, ScheduleOrigin{SessionID: a.Grant.SessionID, RunID: a.RunID, ToolCallID: input.ToolCallID})
			if e != nil {
				return e
			}
			out.Receipt = &receipt
			return nil
		case "get":
			if !validID(input.ID) || input.HistoryLimit < 0 || input.HistoryLimit > 20 {
				return ErrInvalid
			}
			task, e := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=? AND project_id=?`, input.ID, scope.ProjectID))
			if e != nil {
				return e
			}
			out.Schedule = &task
			rows, e := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.schedule_id=? AND r.project_id=? ORDER BY r.created_at DESC,r.id DESC LIMIT ?`, input.ID, scope.ProjectID, input.HistoryLimit)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				run, e := scanScheduleRun(rows)
				if e != nil {
					return e
				}
				out.Runs = append(out.Runs, run)
			}
			return rows.Err()
		case "list":
			page, e := scheduleList(ctx, tx, scope, input.After, input.Limit)
			if e != nil {
				return e
			}
			out.Schedules, out.Next = page.Schedules, page.Next
			return nil
		default:
			return ErrInvalid
		}
	})
	return
}
