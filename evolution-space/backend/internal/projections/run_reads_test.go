package projections

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
)

func TestRunHistoryFiltersBeforePaginationAndKeepsGroupsSeparate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 12; i++ {
		group, state, template := "alpha", "completed", "skills"
		if i%2 == 1 {
			group = "beta"
		}
		if i == 4 {
			state = "failed"
		}
		if i == 6 {
			template = "corrections"
		}
		id := fmt.Sprintf("run-%02d", i)
		run := evolution.Run{ID: id, GroupID: group, State: state, CreatedAt: time.Date(2026, 10, 17, i, 0, 0, 0, time.UTC), Pins: evolution.RunPins{Template: evolution.Revision{ID: template}, Workflow: evolution.Revision{ID: "workflow", Revision: 2}, Base: evolution.ContentRef{CommitID: strings.Repeat("a", 64)}, Inputs: []evolution.ArtifactRef{{ID: "snapshot"}}}}
		raw, _ := json.Marshal(run)
		if _, err = s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES(?,?,?,1,?,'{}',?,?)", id, group, id, id, raw, state); err != nil {
			t.Fatal(err)
		}
		assignment, _ := json.Marshal(evolution.GenerationAssignment{Requester: evolution.Principal{Subject: "owner"}})
		if i%4 == 0 {
			_, err = s.db.ExecContext(ctx, "INSERT INTO generation_admissions VALUES(?,?)", id, assignment)
		} else {
			_, err = s.db.ExecContext(ctx, "INSERT INTO generation_grants VALUES(?,?,'{}')", id, assignment)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	filter := RunFilter{State: "completed", Template: "skills", Workflow: "workflow", WorkflowRevision: 2, BaseCommit: strings.Repeat("a", 64), Source: "snapshot", Author: "owner", From: "2026-10-17T02:00:00Z", Until: "2026-10-17T11:00:00Z"}
	first, err := s.Runs(ctx, "alpha", "", 2, filter)
	if err != nil || len(first.Items) != 2 || !first.HasMore || first.Items[0].ID != "run-02" || first.NextAfter != "run-08" {
		t.Fatal(first, err)
	}
	next, err := s.Runs(ctx, "alpha", first.NextAfter, 2, filter)
	if err != nil || len(next.Items) != 1 || next.HasMore || next.Items[0].ID != "run-10" {
		t.Fatal(next, err)
	}
	// A foreign cursor is only a position; it never grants access to that run.
	page, err := s.Runs(ctx, "alpha", "run-09", 25, RunFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].GroupID != "alpha" {
		t.Fatal(page, err)
	}
	for _, f := range []RunFilter{{State: "succeeded"}, {WorkflowRevision: 1}, {WorkflowRevision: -1}, {BaseCommit: strings.Repeat("a", 40)}, {From: "yesterday"}, {From: "2026-10-17T02:00:00Z", Until: "2026-10-17T02:00:00Z"}, {Author: "x' OR 1=1 --"}} {
		if _, err = s.Runs(ctx, "alpha", "", 25, f); err == nil {
			t.Fatal("invalid filter accepted", f)
		}
	}
	for _, f := range []RunFilter{{Template: "missing"}, {Source: "missing"}, {Author: "missing"}, {WorkflowRevision: 3, Workflow: "workflow"}, {BaseCommit: strings.Repeat("b", 64)}} {
		page, err := s.Runs(ctx, "alpha", "", 25, f)
		if err != nil || len(page.Items) != 0 || page.HasMore {
			t.Fatal("nonmatching metadata leaked", page, err)
		}
	}
	// Time ranges compare instants, including explicit offsets, and exclude until.
	page, err = s.Runs(ctx, "alpha", "", 25, RunFilter{From: "2026-10-17T10:00:00+08:00", Until: "2026-10-17T12:00:00+08:00"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "run-02" {
		t.Fatal(page, err)
	}
}
