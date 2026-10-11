package product

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestAccountSuspensionPersistsAutomationAndShareFencesAcrossEnable(t *testing.T) {
	s, scope, incarnation, running := agentFixture(t)
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id='bob'`)
	must(t, err)
	admin, err := s.Account(ctx, "bob")
	must(t, err)
	_, token, err := s.Authenticate(ctx, admin.Username, "fixture-password")
	must(t, err)
	owner, err := s.Account(ctx, scope.Actor)
	must(t, err)
	must(t, s.GrantConversation(ctx, scope, incarnation.SessionID, "bob"))
	service := &Shares{Store: s, Directory: t.TempDir()}
	share, public, err := service.Create(ctx, scope, incarnation.SessionID, nil, time.Now().Add(time.Hour))
	must(t, err)
	schedule, err := s.CreateSchedule(ctx, scope, Schedule{ID: "scheduled-suspension", Title: "Task", Prompt: "do work", TriggerKind: "interval", Cron: "@every 1m", Timezone: "UTC", State: "active"})
	must(t, err)
	queued, err := s.AdmitTurn(ctx, scope, incarnation.SessionID, "queued-suspended", "unstarted", false)
	must(t, err)
	held, err := s.AdmitTurn(ctx, scope, incarnation.SessionID, "held-suspended", "already held", true)
	must(t, err)
	input := AccountRevision{RequestID: "suspend-with-resources", ExpectedRevision: owner.Revision}
	suspended, err := s.SuspendAccount(ctx, token, scope.Actor, input)
	must(t, err)
	if suspended.Operation.State != "accepted" {
		t.Fatal("logical fences pretended compute stopped")
	}
	var state, reason string
	must(t, s.reader.QueryRow(`SELECT state,last_error FROM schedules WHERE id=?`, schedule.ID).Scan(&state, &reason))
	if state != "paused" || reason != "account_suspended" {
		t.Fatal(state, reason)
	}
	for _, id := range []string{queued.ID, held.ID} {
		must(t, s.reader.QueryRow(`SELECT queue_policy,dispatch_error FROM turns WHERE id=?`, id).Scan(&state, &reason))
		if state != "held" || reason != "account_suspended" {
			t.Fatal(state, reason)
		}
	}
	must(t, s.reader.QueryRow(`SELECT state FROM agent_commands WHERE id=?`, running.CommandID).Scan(&state))
	if state != "sending" {
		t.Fatal("changed already dispatched work", state)
	}
	wantError(t, s.CanReadConversation(ctx, "bob", incarnation.SessionID), ErrNotFound)
	_, err = s.PublicShare(ctx, public)
	wantError(t, err, ErrNotFound)
	_, err = s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable-with-resources", ExpectedRevision: suspended.User.Revision})
	must(t, err)
	// Account enable alone cannot bring back sharing or automation.
	wantError(t, s.CanReadConversation(ctx, "bob", incarnation.SessionID), ErrNotFound)
	_, err = s.PublicShare(ctx, public)
	wantError(t, err, ErrNotFound)
	readers, err := s.ConversationReaders(ctx, scope, incarnation.SessionID, "", 10)
	must(t, err)
	if len(readers) != 1 || !readers[0].Suspended {
		t.Fatal(readers)
	}
	shares, err := s.Shares(ctx, scope, incarnation.SessionID, "", 10)
	must(t, err)
	if len(shares) != 1 || !shares[0].Suspended || shares[0].State != "published" {
		t.Fatal(shares)
	}
	must(t, service.Reconcile(ctx, 10))
	must(t, s.reader.QueryRow(`SELECT state,last_error FROM schedules WHERE id=?`, schedule.ID).Scan(&state, &reason))
	if state != "paused" || reason != "account_suspended" {
		t.Fatal(state, reason)
	}
	wantError(t, s.GrantConversation(ctx, scope, incarnation.SessionID, "bob"), ErrConflict)
	must(t, s.ReactivateConversation(ctx, scope, incarnation.SessionID, "bob", "restore-reader"))
	must(t, s.CanReadConversation(ctx, "bob", incarnation.SessionID))
	must(t, s.ReactivateShare(ctx, scope, share.ID, "restore-public"))
	_, err = s.PublicShare(ctx, public)
	must(t, err)
	// A retry of the prior successful reactivation cannot undo a later suspend.
	current, err := s.Account(ctx, scope.Actor)
	must(t, err)
	again, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "suspend-again", ExpectedRevision: current.Revision})
	must(t, err)
	_, err = s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable-again", ExpectedRevision: again.User.Revision})
	must(t, err)
	must(t, s.ReactivateShare(ctx, scope, share.ID, "restore-public"))
	must(t, s.ReactivateConversation(ctx, scope, incarnation.SessionID, "bob", "restore-reader"))
	wantError(t, s.CanReadConversation(ctx, "bob", incarnation.SessionID), ErrNotFound)
	_, err = s.PublicShare(ctx, public)
	wantError(t, err, ErrNotFound)
	must(t, s.RevokeShare(ctx, scope, share.ID))
	wantError(t, s.ReactivateShare(ctx, scope, share.ID, "restore-revoked"), ErrNotFound)
}
func TestSuspensionAuditFailureRollsBackAllFences(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id='bob'`)
	must(t, err)
	admin, err := s.Account(ctx, "bob")
	must(t, err)
	_, token, err := s.Authenticate(ctx, admin.Username, "fixture-password")
	must(t, err)
	_, err = s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	owner, err := s.Account(ctx, scope.Actor)
	must(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER suspend_audit_fault BEFORE INSERT ON audit_events WHEN NEW.action='account.suspend' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	must(t, err)
	if _, err = s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "rollback-suspend", ExpectedRevision: owner.Revision}); err == nil {
		t.Fatal("audit failure ignored")
	}
	must(t, s.CanReadConversation(ctx, "bob", "shared"))
	owner, err = s.Account(ctx, scope.Actor)
	must(t, err)
	if owner.Status != "active" {
		t.Fatal(owner)
	}
}

func TestSuspendedPublishedSnapshotRetainsFrozenBytes(t *testing.T) {
	files, scope, _ := fileFixture(t)
	defer files.Close()
	s := files.Store
	_, err := s.db.Exec(`UPDATE accounts SET role='admin' WHERE id='bob'`)
	must(t, err)
	admin, err := s.Account(ctx, "bob")
	must(t, err)
	_, token, err := s.Authenticate(ctx, admin.Username, "fixture-password")
	must(t, err)
	_, err = s.CreateSession(ctx, scope, "snapshot-session", "Snapshot")
	must(t, err)
	_, err = files.Upload(ctx, scope, "upload-snapshot", writeRequest("result.txt", "frozen bytes"), strings.NewReader("frozen bytes"))
	must(t, err)
	shares := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	share, public, err := shares.Create(ctx, scope, "snapshot-session", []string{"result.txt"}, time.Now().Add(time.Hour))
	must(t, err)
	owner, err := s.Account(ctx, scope.Actor)
	must(t, err)
	result, err := s.SuspendAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "suspend-snapshot", ExpectedRevision: owner.Revision})
	must(t, err)
	must(t, shares.Reconcile(ctx, 10))
	_, _, err = shares.OpenFile(ctx, public, "result.txt")
	wantError(t, err, ErrNotFound)
	_, err = s.EnableAccount(ctx, token, scope.Actor, AccountRevision{RequestID: "enable-snapshot", ExpectedRevision: result.User.Revision})
	must(t, err)
	_, err = files.Upload(ctx, scope, "change-live-file", writeRequest("result.txt", "new private bytes"), strings.NewReader("new private bytes"))
	must(t, err)
	must(t, s.ReactivateShare(ctx, scope, share.ID, "restore-snapshot"))
	file, _, err := shares.OpenFile(ctx, public, "result.txt")
	must(t, err)
	defer file.Close()
	data, err := io.ReadAll(file)
	must(t, err)
	if string(data) != "frozen bytes" {
		t.Fatal("reactivation recaptured or removed content", string(data))
	}
}
