package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAdminCreationReplayAndReceiptsNeverPersistCredentials(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	none := []string{}
	input := AccountCreate{RequestID: "create-once", Username: "new-user", DisplayName: "New user", Role: "user", GroupIDs: &none}
	start := make(chan struct{})
	results := make(chan AccountMutation, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := s.CreateAccount(ctx, token, input)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var original AccountMutation
	replays := 0
	passwords := []string{}
	for result := range results {
		if result.Replayed {
			replays++
		} else {
			original = result
		}
		if result.TemporaryPassword != "" {
			passwords = append(passwords, result.TemporaryPassword)
		}
	}
	if replays != 1 || len(passwords) != 1 || len(passwords[0]) != 24 || len(original.User.Groups) != 0 {
		t.Fatal("creation was not single-display/idempotent", replays, len(passwords), original.User.Groups)
	}
	replay, err := s.CreateAccount(ctx, token, input)
	if err != nil || !replay.Replayed || replay.User.ID != original.User.ID || replay.TemporaryPassword != "" {
		t.Fatal(replay, err)
	}
	input.DisplayName = "changed"
	if _, err = s.CreateAccount(ctx, token, input); !errors.Is(err, ErrConflict) {
		t.Fatal("changed body reused request ID", err)
	}
	name := "Updated"
	edited, err := s.UpdateAccount(ctx, token, original.User.ID, AccountEdit{RequestID: "edit-once", ExpectedRevision: original.User.Revision, DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	originalRequest := AccountEdit{RequestID: "edit-once", ExpectedRevision: original.User.Revision, DisplayName: &name}
	repeated, err := s.UpdateAccount(ctx, token, original.User.ID, originalRequest)
	if err != nil || !repeated.Replayed || repeated.User.Revision != edited.User.Revision {
		t.Fatal("stale revision beat receipt replay", repeated, err)
	}
	if _, err = s.UpdateAccount(ctx, token, admin.ID, originalRequest); !errors.Is(err, ErrConflict) {
		t.Fatal("request ID changed targets", err)
	}
	record, err := readAccount(ctx, s.reader, "id", original.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"admin_receipts", "audit_events"} {
		column := "result"
		if table == "audit_events" {
			column = "details"
		}
		rows, err := s.reader.QueryContext(ctx, `SELECT `+column+` FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, passwords[0]) || strings.Contains(raw, record.hash) || strings.Contains(raw, "password_hash") || strings.Contains(raw, "temporary_password") {
				t.Fatal("credential material persisted in administrative outcome")
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
}
func TestAdminResetReplayDoesNotRotateAgainAndAuthorizationIsCurrent(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	created, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: "create", Username: "user", DisplayName: "User", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	request := AccountRevision{RequestID: "reset-once", ExpectedRevision: created.User.Revision}
	reset, err := s.ResetAccountPassword(ctx, token, created.User.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ResetAccountPassword(ctx, token, created.User.ID, request)
	if err != nil || !replay.Replayed || replay.TemporaryPassword != "" {
		t.Fatal(replay, err)
	}
	if _, _, err = s.Authenticate(ctx, "user", created.TemporaryPassword); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old credential survived reset", err)
	}
	_, setup, err := s.Authenticate(ctx, "user", reset.TemporaryPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, userToken, err := s.ChangePassword(ctx, setup, "", "permanent-user-password", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdminUsers(ctx, userToken, AdminUserQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user listed admin data", err)
	}
	if _, err = s.CreateAccount(ctx, userToken, AccountCreate{RequestID: "create-admin", Username: "forged", DisplayName: "Forged", Role: "admin"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user created an admin", err)
	}
	if err = s.RevokeAllLogins(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResetAccountPassword(ctx, token, created.User.ID, request); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked authority read a receipt", err)
	}
}
func TestAdminPaginationFiltersBeforePageAndAuditIsBounded(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	// Fixture identities use the same record constructor and a valid stored hash;
	// this test exercises query volume rather than repeating expensive hashing.
	source, err := readAccount(ctx, s.reader, "id", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 105; i++ {
			a, err := s.insertAccount(ctx, tx, fmt.Sprintf("user-%03d", i), fmt.Sprintf("Member %03d", i), "user", source.hash, false)
			if err != nil {
				return err
			}
			if i%2 == 0 {
				if _, err = tx.ExecContext(ctx, `INSERT INTO account_memberships(account_id,group_id) VALUES(?,?)`, a.ID, StandardUsersGroup); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.AdminUsers(ctx, token, AdminUserQuery{Search: "Member", Role: "user", Limit: 17, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Users) > 17 {
			t.Fatal("page exceeded limit")
		}
		for _, user := range page.Users {
			if seen[user.ID] || user.Role != "user" {
				t.Fatal("duplicate/unfiltered user")
			}
			seen[user.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 105 {
		t.Fatal("paging lost users", len(seen))
	}
	if _, err = s.AdminUsers(ctx, token, AdminUserQuery{Search: "Administrator", Cursor: cursor}); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor changed filter", err)
	}
	if _, err = s.AdminUsers(ctx, token, AdminUserQuery{Limit: 101}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	page, err := s.AdminUsers(ctx, token, AdminUserQuery{Role: "admin", Limit: 1})
	if err != nil || len(page.Users) != 1 || page.Users[0].LastLoginAt == "" {
		t.Fatal(page, err)
	}
	audit, err := s.AdminAudit(ctx, token, AuditQuery{Actor: admin.ID, Action: "account.login", Limit: 1})
	if err != nil || len(audit.Events) != 1 || audit.Events[0].Action != "account.login" {
		t.Fatal(audit, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "token") {
		t.Fatal("admin query exposed secret fields")
	}
}
func TestSuspensionRecordsIntentWithoutClaimingRemoteCompletion(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	created, err := s.CreateAccount(ctx, token, AccountCreate{RequestID: uuid.NewString(), Username: "user", DisplayName: "User", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, created.User.ID, "owned-project", "Owned"); err != nil {
		t.Fatal(err)
	}
	input := AccountRevision{RequestID: "suspend", ExpectedRevision: created.User.Revision}
	result, err := s.SuspendAccount(ctx, token, created.User.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.User.Status != "disabled" || result.Operation == nil || result.Operation.State != "accepted" || result.Operation.Total != 1 {
		t.Fatal(result)
	}
	again, err := s.SuspendAccount(ctx, token, created.User.ID, input)
	if err != nil || !again.Replayed || again.Operation.ID != result.Operation.ID {
		t.Fatal(again, err)
	}
	current, err := s.AdminOperation(ctx, token, result.Operation.ID)
	if err != nil || current.State != "accepted" || current.Completed != 0 {
		t.Fatal(current, err)
	}
	if _, _, err = s.Authenticate(ctx, "user", created.TemporaryPassword); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled account logged in", err)
	}
	enabled, err := s.EnableAccount(ctx, token, created.User.ID, AccountRevision{RequestID: "enable", ExpectedRevision: result.User.Revision})
	if err != nil || enabled.User.Status != "active" {
		t.Fatal(enabled, err)
	}
	current, err = s.AdminOperation(ctx, token, result.Operation.ID)
	if err != nil || current.State != "accepted" {
		t.Fatal("enable fabricated suspension completion", current, err)
	}
}

func TestAdminAuditFailureRollsBackIdentityAndReceipt(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	if _, err := s.db.ExecContext(ctx, `CREATE TEMP TRIGGER fail_admin_audit BEFORE INSERT ON audit_events WHEN NEW.action='account.create' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	input := AccountCreate{RequestID: "audit-failure", Username: "rolled-back", DisplayName: "Rolled back", Role: "user"}
	result, err := s.CreateAccount(ctx, token, input)
	if err == nil || result.TemporaryPassword != "" {
		t.Fatal("failed creation produced credentials")
	}
	for _, query := range []string{`SELECT count(*) FROM accounts WHERE username='rolled-back'`, `SELECT count(*) FROM admin_receipts WHERE request_id='audit-failure'`} {
		var count int
		if err = s.reader.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial administrative mutation", count, err)
		}
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_admin_audit`); err != nil {
		t.Fatal(err)
	}
	if result, err = s.CreateAccount(ctx, token, input); err != nil || result.Replayed || result.User.ID == "" {
		t.Fatal("safe retry after rollback failed", err)
	}
}

func TestAdminReceiptsSurviveOwnerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-restart.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", bootstrapSeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	token := adminTestToken(t, store, account)
	input := AccountCreate{RequestID: "before-restart", Username: "retained", DisplayName: "Retained", Role: "user"}
	first, err := store.CreateAccount(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replay, err := store.CreateAccount(ctx, token, input)
	if err != nil || !replay.Replayed || replay.User.ID != first.User.ID || replay.TemporaryPassword != "" {
		t.Fatal("restart lost receipt", err)
	}
	if _, _, err = store.Authenticate(ctx, "retained", first.TemporaryPassword); err != nil {
		t.Fatal("replay replaced original credential", err)
	}
}
func TestAdminRechecksSessionAfterWaitingForWriter(t *testing.T) {
	store, admin := accountStore(t)
	token := adminTestToken(t, store, admin)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	defer once.Do(func() { close(release) })
	go func() {
		done <- store.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_digest=?`, tokenDigest(token)); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	result := make(chan error, 1)
	go func() {
		_, err := store.CreateAccount(ctx, token, AccountCreate{RequestID: "queued", Username: "not-created", DisplayName: "Not created", Role: "user"})
		result <- err
	}()
	deadline := time.Now().Add(15 * time.Second)
	queued := false
	for time.Now().Before(deadline) {
		store.writer.mu.Lock()
		queued = store.writer.stats[controlWrite].Queued > 0
		store.writer.mu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !queued {
		t.Fatal("administrative request did not reach writer queue")
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked session committed queued mutation", err)
	}
	var count int
	if err := store.reader.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE username='not-created'`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
