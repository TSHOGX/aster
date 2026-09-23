package product

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func ownerHandoffFixture(t *testing.T) (*Store, ServiceRequest, string, func() GatewayObservation) {
	t.Helper()
	s, scope, i, c := agentServiceFixture(t)
	s.identity = ApplicationIdentity{ExecutableDigest: "sha256:" + strings.Repeat("a", 64), CompatibilityDigest: "sha256:" + strings.Repeat("c", 64)}
	_, err := s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	registration, err := s.RegisterGateway(ctx, "model", "gateway", 0, 2)
	must(t, err)
	r := ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}
	_, err = s.AcquireGatewayRequest(ctx, r, token)
	must(t, err)
	return s, r, token, func() GatewayObservation {
		return GatewayObservation{Started: time.Now(), Registrations: map[string]GatewayRegistration{"model": registration}}
	}
}

func TestOwnerHandoffTransfersOnlyAppliedNativeWorkAcrossVerifiedReplacement(t *testing.T) {
	s, r, token, observe := ownerHandoffFixture(t)
	target := "sha256:" + strings.Repeat("b", 64)
	full, err := s.BeginReleaseDrain(ctx, "full-drain", time.Minute)
	must(t, err)
	_, err = s.TryReleaseReady(ctx, full.Owner, full.Generation)
	wantError(t, err, ErrBusy)
	_, err = s.AbortReleaseDrain(ctx, full.Owner, full.Generation)
	must(t, err)
	_, err = s.BeginOwnerHandoff(ctx, "invalid-contract", time.Minute, target, "sha256:"+strings.Repeat("d", 64), observe())
	wantError(t, err, ErrInvalid)
	stale := observe()
	stale.Started = time.Now().Add(-3 * time.Second)
	_, err = s.BeginOwnerHandoff(ctx, "stale-gateway", time.Minute, target, s.identity.CompatibilityDigest, stale)
	wantError(t, err, ErrBusy)
	_, err = s.BeginOwnerHandoff(ctx, "omitted-gateway", time.Minute, target, s.identity.CompatibilityDigest, GatewayObservation{Started: time.Now()})
	wantError(t, err, ErrConflict)
	lease, err := s.beginRuntimeWork(ctx, runtimeRead)
	must(t, err)
	drain, err := s.BeginOwnerHandoff(ctx, "owner-drain", time.Minute, target, s.identity.CompatibilityDigest, observe())
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
	wantError(t, err, ErrBusy)
	lease()
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation)
	wantError(t, err, ErrConflict)
	ready, err := s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
	must(t, err)
	if ready.Phase != "ready" || ready.Mode != "owner" || ready.GatewayIdentity == "" {
		t.Fatal(ready)
	}
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if len(status.HandoffBlockers) != 0 || status.DurableBlockers["turn"] != 1 || status.DurableBlockers["model_request"] != 1 {
		t.Fatal("retained work was reported as idle or blocked", status)
	}
	_, err = s.beginRuntimeWork(ctx, runtimeRead)
	wantError(t, err, ErrDraining)
	second := r
	second.Slot = 1
	_, err = s.AcquireGatewayRequest(ctx, second, token)
	must(t, err)
	must(t, s.ReleaseGatewayRequest(ctx, second))
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, s.identity.ExecutableDigest, observe())
	wantError(t, err, ErrConflict)
	active, err := s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, observe())
	must(t, err)
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target, false, observe())
	wantError(t, err, ErrConflict)
	second.Sequence++
	_, err = s.AcquireGatewayRequest(ctx, second, token)
	must(t, err)
	must(t, s.ReleaseGatewayRequest(ctx, second))
	var seq int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	compatibility := s.identity.CompatibilityDigest
	must(t, s.Close())
	before, err := os.ReadFile(path)
	must(t, err)
	for _, identity := range []ApplicationIdentity{
		{ExecutableDigest: target},
		{ExecutableDigest: target, CompatibilityDigest: "sha256:" + strings.Repeat("e", 64)},
		{ExecutableDigest: "sha256:" + strings.Repeat("f", 64), CompatibilityDigest: compatibility},
	} {
		if wrong, e := OpenApplication(ctx, path, identity); e == nil {
			wrong.Close()
			t.Fatal("incompatible handoff candidate admitted")
		}
		after, e := os.ReadFile(path)
		must(t, e)
		if !bytes.Equal(before, after) {
			t.Fatal("rejected handoff candidate changed Store")
		}
	}
	if unmeasured, e := Open(ctx, path); e == nil {
		unmeasured.Close()
		t.Fatal("unmeasured Store bypassed handoff compatibility")
	}
	s, err = OpenApplication(ctx, path, ApplicationIdentity{ExecutableDigest: target, CompatibilityDigest: compatibility})
	must(t, err)
	defer s.Close()
	must(t, s.CheckGatewayRequest(ctx, r))
	work, err := s.beginRuntimeWork(ctx, runtimeMaintenance)
	must(t, err)
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target, false, observe())
	wantError(t, err, ErrBusy)
	work()
	finished, err := s.FinishRelease(ctx, drain.Owner, drain.Generation, target, false, observe())
	must(t, err)
	if finished.Phase != "open" || finished.ActivationInstance != active.ActivationInstance || finished.FinishedDigest != target {
		t.Fatal(finished)
	}
	must(t, s.CheckGatewayRequest(ctx, r))
	must(t, s.ReleaseGatewayRequest(ctx, r))
}

func TestOwnerHandoffRejectsUncertainTransitionsAndUnsettledRequests(t *testing.T) {
	for name, statement := range map[string]string{
		"turn starting":                          `UPDATE turns SET state='starting'`,
		"turn stopping":                          `UPDATE turns SET state='stopping'`,
		"acknowledged but live unknown":          `UPDATE turns SET state='unknown',acknowledged=1`,
		"unknown command":                        `UPDATE agent_commands SET state='unknown'`,
		"dispatching command":                    `UPDATE agent_commands SET state='dispatching'`,
		"incarnation transition":                 `UPDATE agent_incarnations SET state='stopping'`,
		"Project transition":                     `UPDATE projects SET state='pausing'`,
		"revoked grant":                          `UPDATE service_grants SET state='revoked'`,
		"expired grant":                          `UPDATE service_grants SET expires_at='2000-01-01T00:00:00.000000000Z'`,
		"finished but unacknowledged forwarding": `UPDATE turns SET state='completed'`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _, observe := ownerHandoffFixture(t)
			_, err := s.db.Exec(statement)
			must(t, err)
			drain, err := s.BeginOwnerHandoff(ctx, "unsafe", time.Minute, "sha256:"+strings.Repeat("b", 64), s.identity.CompatibilityDigest, observe())
			must(t, err)
			_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
			wantError(t, err, ErrBusy)
		})
	}
}

func TestOwnerHandoffRevokesReadyOnLateWorkOrGatewayReplacement(t *testing.T) {
	for _, replaceGateway := range []bool{false, true} {
		t.Run(fmt.Sprint(replaceGateway), func(t *testing.T) {
			s, _, _, observe := ownerHandoffFixture(t)
			target := "sha256:" + strings.Repeat("b", 64)
			drain, err := s.BeginOwnerHandoff(ctx, "revocation", time.Minute, target, s.identity.CompatibilityDigest, observe())
			must(t, err)
			_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
			must(t, err)
			if replaceGateway {
				_, err = s.RegisterGateway(ctx, "model", "replacement", 1, 2)
				must(t, err)
			} else {
				_, err = s.db.Exec(`UPDATE agent_commands SET state='unknown'`)
				must(t, err)
			}
			state, err := s.ReleaseState(ctx)
			must(t, err)
			if state.Phase != "draining" {
				t.Fatal("unsafe work retained readiness", state)
			}
			_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, observe())
			wantError(t, err, ErrConflict)
			if replaceGateway {
				proof := observe()
				proof.Registrations["model"] = GatewayRegistration{Instance: "replacement", Generation: 2, Slots: 2}
				_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, proof)
				wantError(t, err, ErrConflict)
			}
		})
	}
}

func TestOwnerHandoffActivationCannotRaceUnknownEvidence(t *testing.T) {
	for round := 0; round < 12; round++ {
		s, _, _, observe := ownerHandoffFixture(t)
		target := "sha256:" + strings.Repeat("b", 64)
		drain, err := s.BeginOwnerHandoff(ctx, fmt.Sprint("race-", round), time.Minute, target, s.identity.CompatibilityDigest, observe())
		must(t, err)
		_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
		must(t, err)
		start := make(chan struct{})
		var group sync.WaitGroup
		var activateErr, updateErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, activateErr = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, observe())
		}()
		go func() {
			defer group.Done()
			<-start
			_, updateErr = s.exec(ctx, `UPDATE agent_commands SET state='unknown'`)
		}()
		close(start)
		group.Wait()
		must(t, updateErr)
		state, err := s.ReleaseState(ctx)
		must(t, err)
		if activateErr != nil {
			wantError(t, activateErr, ErrConflict)
			if state.Phase != "draining" {
				t.Fatal("unknown evidence did not revoke ready", state)
			}
		} else if state.Phase != "activating" {
			t.Fatal("activation lost its fence", state)
		}
		// Evidence after activation is still authoritative and cannot be declared
		// transferable merely because retirement has begun.
		status, err := s.ReleaseDrainStatus(ctx)
		must(t, err)
		if status.HandoffBlockers["command"] != 1 {
			t.Fatal("uncertain command disappeared at handoff", status)
		}
	}
}

func TestOwnerHandoffDeadlineReopensAdmissionWithoutCancelingNativeWork(t *testing.T) {
	s, r, _, observe := ownerHandoffFixture(t)
	drain, err := s.BeginOwnerHandoff(ctx, "deadline-owner", time.Second, "sha256:"+strings.Repeat("b", 64), s.identity.CompatibilityDigest, observe())
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
	must(t, err)
	now := s.now()
	s.now = func() time.Time { return now.Add(2 * time.Second) }
	must(t, s.ExpireReleaseDrain(ctx))
	state, err := s.ReleaseState(ctx)
	must(t, err)
	if state.Phase != "open" || state.Reason != "deadline" {
		t.Fatal(state)
	}
	must(t, s.CheckGatewayRequest(ctx, r))
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, drain.TargetDigest, observe())
	wantError(t, err, ErrConflict)
}

func TestOwnerReadyRevocationFollowsChangesToRetainedAuthority(t *testing.T) {
	for name, statement := range map[string]string{
		"turn completes":               `UPDATE turns SET state='completed'`,
		"prompt completes":             `UPDATE agent_commands SET state='completed'`,
		"session blocked":              `UPDATE sessions SET state='blocked'`,
		"grant revoked":                `UPDATE service_grants SET state='revoked'`,
		"grant expired":                `UPDATE service_grants SET expires_at='2000-01-01T00:00:00.000000000Z'`,
		"request loses original grant": `UPDATE service_request_slots SET grant_id='lost' WHERE closed=0`,
		"new orphan request":           `INSERT INTO service_request_slots(audience,slot,sequence,closed,grant_id,turn_id) VALUES('model',1,1,0,'lost','lost')`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _, observe := ownerHandoffFixture(t)
			drain, err := s.BeginOwnerHandoff(ctx, "authority-change", time.Minute, "sha256:"+strings.Repeat("b", 64), s.identity.CompatibilityDigest, observe())
			must(t, err)
			_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
			must(t, err)
			_, err = s.db.Exec(statement)
			must(t, err)
			state, err := s.ReleaseState(ctx)
			must(t, err)
			if state.Phase != "draining" {
				t.Fatal("changed retained authority did not revoke ready", state)
			}
		})
	}
}

func TestOwnerHandoffGatewayLossRequiresQuiescentExplicitRollback(t *testing.T) {
	s, _, _, observe := ownerHandoffFixture(t)
	target := "sha256:" + strings.Repeat("b", 64)
	drain, err := s.BeginOwnerHandoff(ctx, "gateway-loss", time.Minute, target, s.identity.CompatibilityDigest, observe())
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, observe())
	must(t, err)
	_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, observe())
	must(t, err)
	var seq int
	var name, path string
	must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	identity := s.identity
	identity.RollbackCandidate = true
	must(t, s.Close())
	s, err = OpenApplication(ctx, path, identity)
	must(t, err)
	defer s.Close()
	registration, err := s.RegisterGateway(ctx, "model", "replacement", 1, 2)
	must(t, err)
	proof := func() GatewayObservation {
		return GatewayObservation{Started: time.Now(), Registrations: map[string]GatewayRegistration{"model": registration}}
	}
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, identity.ExecutableDigest, true, proof())
	wantError(t, err, ErrBusy)
	_, err = s.db.Exec(`UPDATE turns SET state='completed'; UPDATE agent_commands SET state='completed'`)
	must(t, err)
	_, err = s.FinishRelease(ctx, drain.Owner, drain.Generation, target, false, proof())
	wantError(t, err, ErrConflict)
	finished, err := s.FinishRelease(ctx, drain.Owner, drain.Generation, identity.ExecutableDigest, true, proof())
	must(t, err)
	if finished.Phase != "open" || finished.Reason != "rolled_back" {
		t.Fatal(finished)
	}
}
