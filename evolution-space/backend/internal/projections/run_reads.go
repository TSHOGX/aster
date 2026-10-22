package projections

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Run reads one group's retained run. Callers must authorize current group access
// before invoking this internal storage API; immutable admission is not a grant.
func (s *Store) Run(ctx context.Context, group, id string) (evolution.Run, error) {
	var result evolution.Run
	if !manifest.ValidID(group) || !manifest.ValidID(id) {
		return result, fmt.Errorf("invalid run scope")
	}
	var body []byte
	var state string
	var fence int64
	err := s.db.QueryRowContext(ctx, "SELECT body,state,fence FROM runs WHERE group_id=? AND id=?", group, id).Scan(&body, &state, &fence)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	result, err = evolution.Decode[evolution.Run](bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	if result.ID != id || result.GroupID != group {
		return evolution.Run{}, fmt.Errorf("stored run identity mismatch")
	}
	result.State = state
	result.Fence = fence
	if result.DraftTest != nil {
		result.Handoff, result.EvaluationState, result.ResultBranch = "not_applicable", "not_applicable", ""
		return result, nil
	}
	handoff, err := s.Handoff(ctx, group, id)
	if err == nil {
		result.Handoff = handoff.State
		result.ResultBranch = handoff.BranchID
	} else if !errors.Is(err, ErrNotFound) {
		return evolution.Run{}, err
	} else {
		result.ResultBranch = ""
		result.Handoff = "not_ready"
		if result.State == "completed" {
			result.Handoff = "pending"
		}
	}
	return result, nil
}

type RunPage struct {
	Items     []evolution.Run `json:"items"`
	NextAfter string          `json:"next_after"`
	HasMore   bool            `json:"has_more"`
}

type RunFilter struct {
	State            string
	Template         string
	Workflow         string
	WorkflowRevision int64
	BaseCommit       string
	Source           string
	Author           string
	From             string
	Until            string
}

func (f RunFilter) Validate() error {
	if f.State != "" {
		switch f.State {
		case "queued", "running", "pause_requested", "paused", "cancelling", "completed", "failed", "cancelled":
		default:
			return fmt.Errorf("invalid run state")
		}
	}
	for _, id := range []string{f.Template, f.Workflow, f.Source, f.Author} {
		if id != "" && !manifest.ValidID(id) {
			return fmt.Errorf("invalid run filter identity")
		}
	}
	if f.WorkflowRevision < 0 || f.WorkflowRevision > 0 && f.Workflow == "" {
		return fmt.Errorf("workflow revision requires workflow")
	}
	if f.BaseCommit != "" && !evolution.ObjectID(f.BaseCommit) {
		return fmt.Errorf("invalid base commit")
	}
	var from, until time.Time
	var err error
	if f.From != "" {
		from, err = time.Parse(time.RFC3339, f.From)
		if err != nil {
			return fmt.Errorf("invalid time range")
		}
	}
	if f.Until != "" {
		until, err = time.Parse(time.RFC3339, f.Until)
		if err != nil {
			return fmt.Errorf("invalid time range")
		}
	}
	if !from.IsZero() && !until.IsZero() && !from.Before(until) {
		return fmt.Errorf("invalid time range")
	}
	return nil
}

// Runs returns bounded group-scoped metadata in stable ID order. It deliberately
// reuses Run so handoff and evaluation states have the same authority snapshot as
// the detail view.
func (s *Store) Runs(ctx context.Context, group, after string, limit int, filter RunFilter) (RunPage, error) {
	result := RunPage{Items: []evolution.Run{}, NextAfter: after}
	if !manifest.ValidID(group) || after != "" && !manifest.ValidID(after) || limit < 1 || limit > 50 {
		return result, fmt.Errorf("invalid run page")
	}
	if err := filter.Validate(); err != nil {
		return result, err
	}
	where := []string{"r.group_id=?", "r.id>?"}
	args := []any{group, after}
	add := func(clause string, value any) { where = append(where, clause); args = append(args, value) }
	if filter.State != "" {
		add("r.state=?", filter.State)
	}
	if filter.Template != "" {
		add("json_extract(r.body,'$.pins.template.id')=?", filter.Template)
	}
	if filter.Workflow != "" {
		add("json_extract(r.body,'$.pins.workflow.id')=?", filter.Workflow)
	}
	if filter.WorkflowRevision > 0 {
		add("json_extract(r.body,'$.pins.workflow.revision')=?", filter.WorkflowRevision)
	}
	if filter.BaseCommit != "" {
		add("json_extract(r.body,'$.pins.base.commit_id')=?", filter.BaseCommit)
	}
	if filter.Source != "" {
		add("EXISTS(SELECT 1 FROM json_each(r.body,'$.pins.inputs') i WHERE json_extract(i.value,'$.id')=?)", filter.Source)
	}
	if filter.Author != "" {
		add("coalesce((SELECT json_extract(a.assignment,'$.requester.subject') FROM generation_admissions a WHERE a.run_id=r.id),(SELECT json_extract(g.assignment,'$.requester.subject') FROM generation_grants g WHERE g.run_id=r.id))=?", filter.Author)
	}
	if filter.From != "" {
		add("julianday(json_extract(r.body,'$.created_at'))>=julianday(?)", filter.From)
	}
	if filter.Until != "" {
		add("julianday(json_extract(r.body,'$.created_at'))<julianday(?)", filter.Until)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT r.id FROM runs r WHERE "+strings.Join(where, " AND ")+" ORDER BY r.id LIMIT ?", args...)
	if err != nil {
		return result, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return result, err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	if len(ids) > limit {
		ids = ids[:limit]
		result.HasMore = true
	}
	for _, id := range ids {
		value, readErr := s.Run(ctx, group, id)
		if readErr != nil {
			return result, readErr
		}
		result.Items = append(result.Items, value)
		result.NextAfter = id
	}
	return result, rows.Err()
}
