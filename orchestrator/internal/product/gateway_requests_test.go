package product

import (
	"testing"
	"time"
)

func TestGatewayRequestSurvivesOwnerRestartAndBlocksReadyUntilAcknowledged(t *testing.T) {
	s, scope, i, command := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	_, err = s.RegisterGateway(ctx, "model", "gateway-a", 0, 2)
	must(t, err)
	r := ServiceRequest{Audience: "model", Instance: "gateway-a", Slot: 0, Sequence: 1}
	a, err := s.AcquireGatewayRequest(ctx, r, token)
	must(t, err)
	if a.Request == nil || *a.Request != r || a.TurnID != command.TurnID {
		t.Fatal(a)
	}
	state, err := s.BeginReleaseDrain(ctx, "gateway-drain", time.Minute)
	must(t, err)
	second := r
	second.Slot = 1
	_, err = s.AcquireGatewayRequest(ctx, second, token)
	must(t, err)
	must(t, s.ReleaseGatewayRequest(ctx, second))
	var number int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&number, &name, &path))
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	must(t, s.CheckGatewayRequest(ctx, r))
	ingestContract(t, s, i, command, "native", consumedPayload(command))
	_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE prompt_id=?`, command.TurnID)
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_commands SET state='completed' WHERE id=?`, command.CommandID)
	must(t, err)
	wantError(t, s.CheckGatewayRequest(ctx, r), ErrNotFound)
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.DurableBlockers["model_request"] != 1 {
		t.Fatal(status)
	}
	_, err = s.TryReleaseReady(ctx, state.Owner, state.Generation)
	wantError(t, err, ErrBusy)
	// Even much later the absence of an acknowledgment is not proof of idleness.
	now := s.now()
	s.now = func() time.Time { return now.Add(30 * time.Second) }
	_, err = s.TryReleaseReady(ctx, state.Owner, state.Generation)
	wantError(t, err, ErrBusy)
	must(t, s.ReleaseGatewayRequest(ctx, r))
	_, err = s.TryReleaseReady(ctx, state.Owner, state.Generation)
	must(t, err)
	r.Sequence++
	_, err = s.AcquireGatewayRequest(ctx, r, token)
	wantError(t, err, ErrDraining)
}

func TestGatewaySlotsFenceDelayedAcquireReleaseAndRegistration(t *testing.T) {
	s, scope, i, _ := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	registered, err := s.RegisterGateway(ctx, "model", "original", 0, 1)
	must(t, err)
	r := ServiceRequest{Audience: "model", Instance: "original", Slot: 0, Sequence: 1}
	// The release response overtakes an acquire whose outcome was lost.
	must(t, s.ReleaseGatewayRequest(ctx, r))
	_, err = s.AcquireGatewayRequest(ctx, r, token)
	wantError(t, err, ErrConflict)
	r.Sequence++
	_, err = s.AcquireGatewayRequest(ctx, r, token)
	must(t, err)
	_, err = s.AcquireGatewayRequest(ctx, r, token)
	must(t, err)
	old := r
	old.Sequence = 1
	must(t, s.ReleaseGatewayRequest(ctx, old))
	must(t, s.CheckGatewayRequest(ctx, r))
	bad := r
	bad.Slot = 1
	_, err = s.AcquireGatewayRequest(ctx, bad, token)
	wantError(t, err, ErrConflict)
	// Registering the same process never clears its active requests.
	_, err = s.RegisterGateway(ctx, "model", "original", 0, 1)
	must(t, err)
	must(t, s.CheckGatewayRequest(ctx, r))
	_, err = s.RegisterGateway(ctx, "model", "replacement", registered.Generation, 1)
	must(t, err)
	wantError(t, s.CheckGatewayRequest(ctx, r), ErrNotFound)
	wantError(t, s.ReleaseGatewayRequest(ctx, r), ErrConflict)
	_, err = s.RegisterGateway(ctx, "model", "late-original", registered.Generation, 1)
	wantError(t, err, ErrConflict)
	r.Instance = "replacement"
	r.Sequence = 1
	for n := 0; n < 30; n++ {
		_, err = s.AcquireGatewayRequest(ctx, r, token)
		must(t, err)
		must(t, s.ReleaseGatewayRequest(ctx, r))
		r.Sequence++
	}
	var rows int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM service_request_slots`).Scan(&rows))
	if rows != 1 {
		t.Fatal("slot metadata grew with requests", rows)
	}
}

func TestGatewayCapacityIsGlobalAndDeniedTokensRetainNoRequest(t *testing.T) {
	s, scope, i, _ := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	_, err = s.RegisterGateway(ctx, "model", "gateway", 0, 256)
	must(t, err)
	for n := 0; n < 256; n++ {
		r := ServiceRequest{Audience: "model", Instance: "gateway", Slot: n, Sequence: 1}
		_, err = s.AcquireGatewayRequest(ctx, r, token)
		must(t, err)
	}
	_, err = s.RegisterGateway(ctx, "another", "gateway-another", 0, 1)
	must(t, err)
	_, err = s.db.Exec(`UPDATE service_grants SET audience='another'`)
	must(t, err)
	_, err = s.AcquireGatewayRequest(ctx, ServiceRequest{Audience: "another", Instance: "gateway-another", Slot: 0, Sequence: 1}, token)
	wantError(t, err, ErrCapacity)
	_, err = s.db.Exec(`UPDATE service_grants SET audience='model'`)
	must(t, err)
	// The bounded active index contains only live requests, not completed history.
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM release_blockers WHERE kind='model_request'`).Scan(&count))
	if count != 256 {
		t.Fatal(count)
	}
	for n := 0; n < 256; n++ {
		must(t, s.ReleaseGatewayRequest(ctx, ServiceRequest{Audience: "model", Instance: "gateway", Slot: n, Sequence: 1}))
	}
	_, err = s.AcquireGatewayRequest(ctx, ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 2}, "wrong")
	wantError(t, err, ErrNotFound)
	must(t, s.reader.QueryRow(`SELECT count(*) FROM release_blockers WHERE kind='model_request'`).Scan(&count))
	if count != 0 {
		t.Fatal("denied request admitted:", count)
	}
}
