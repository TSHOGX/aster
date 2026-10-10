package product

import (
	"testing"
	"time"
)

// PlatformFixtureForTest shares the real live-grant fixture with the external
// transport test without exporting production-only fixture APIs.
func PlatformFixtureForTest(t *testing.T) (*Store, Scope, string) {
	t.Helper()
	s, scope, i, _ := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	return s, scope, token
}
func TestPlatformScheduleAuthorityFencesEveryMutation(t *testing.T) {
	for _, boundary := range []string{"revoked", "expired", "stopping", "idle", "owner", "slot", "gateway", "run", "draining", "foreign"} {
		t.Run(boundary, func(t *testing.T) {
			s, scope, i, _ := agentServiceFixture(t)
			grant, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
			must(t, err)
			_, err = s.RegisterGateway(ctx, "model", "gateway", 0, 1)
			must(t, err)
			slot := ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}
			a, err := s.AcquireGatewayRequest(ctx, slot, token)
			must(t, err)
			context, err := s.PlatformSchedules(ctx, slot, PlatformScheduleRequest{Action: "context", Timezone: "Asia/Shanghai"})
			must(t, err)
			if context.Context.RunID != a.RunID || context.Context.SessionID != i.SessionID || context.Context.ProjectID != scope.ProjectID {
				t.Fatal(context)
			}
			definition := oneShotMutation("future", s.now().Add(time.Hour).Format(time.RFC3339))
			request := PlatformScheduleRequest{Action: "execute", Operation: "create", ID: definition.ID, RequestID: definition.RequestID, ExpectedRunID: a.RunID, ToolCallID: "call", Patch: definition.Patch}
			switch boundary {
			case "revoked":
				must(t, s.RevokeService(ctx, scope, grant.ID))
			case "expired":
				_, err = s.db.Exec(`UPDATE service_grants SET expires_at=? WHERE id=?`, s.timestamp(), grant.ID)
			case "stopping":
				_, err = s.db.Exec(`UPDATE execution_runs SET state='stopping' WHERE id=?`, a.RunID)
				must(t, err)
				must(t, s.CheckGatewayRequest(ctx, slot))
			case "idle":
				_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE id=?`, a.RunID)
			case "owner":
				_, err = s.db.Exec(`UPDATE agent_incarnations SET owner=owner+1 WHERE id=?`, i.ID)
			case "slot":
				must(t, s.ReleaseGatewayRequest(ctx, slot))
			case "gateway":
				_, err = s.RegisterGateway(ctx, "model", "replacement", 1, 1)
			case "run":
				request.ExpectedRunID = "successor"
			case "draining":
				_, err = s.BeginReleaseDrain(ctx, "release", time.Minute)
			case "foreign":
				request.Target = &ScheduleTarget{Kind: "session", SessionID: "foreign"}
			}
			must(t, err)
			_, err = s.PlatformSchedules(ctx, slot, request)
			if err == nil {
				t.Fatal("authority fence accepted", boundary)
			}
			_, err = s.Schedule(ctx, scope, request.ID)
			wantError(t, err, ErrNotFound)
		})
	}
}
func TestPlatformScheduleDefaultsAndMetadataBounds(t *testing.T) {
	s, scope, token := PlatformFixtureForTest(t)
	_, err := s.RegisterGateway(ctx, "model", "gateway", 0, 1)
	must(t, err)
	slot := ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}
	a, err := s.AcquireGatewayRequest(ctx, slot, token)
	must(t, err)
	input := oneShotMutation("future", s.now().Add(time.Hour).Format(time.RFC3339))
	request := PlatformScheduleRequest{Action: "execute", Operation: "create", ID: input.ID, RequestID: input.RequestID, ExpectedRunID: a.RunID, ToolCallID: "call", Patch: input.Patch}
	result, err := s.PlatformSchedules(ctx, slot, request)
	must(t, err)
	task := result.Receipt.Schedule
	if task.SessionID != a.Grant.SessionID || task.OriginSessionID != a.Grant.SessionID || task.OriginRunID != a.RunID || !task.Notify {
		t.Fatal(task)
	}
	request.Operation = "list"
	request.Limit = 1
	result, err = s.PlatformSchedules(ctx, slot, request)
	must(t, err)
	if len(result.Schedules) != 1 || result.Schedules[0].ID != task.ID {
		t.Fatal(result)
	}
	request.Operation = "get"
	request.HistoryLimit = 21
	_, err = s.PlatformSchedules(ctx, slot, request)
	wantError(t, err, ErrInvalid)
	request.Operation = "delete"
	_, err = s.PlatformSchedules(ctx, slot, request)
	wantError(t, err, ErrInvalid)
	current, err := s.Schedule(ctx, scope, task.ID)
	must(t, err)
	if current.State != "active" {
		t.Fatal(current)
	}
}
