package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/testfixture"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func accountStore(t *testing.T) (*Store, Account) {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, err := s.BootstrapAccount(context.Background(), "Admin", "permanent-admin-password", bootstrapSeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}
func TestAccountBootstrapAtomicAndRestart(t *testing.T) {
	ctx := context.Background()
	name := filepath.Join(t.TempDir(), "identity.db")
	s, err := Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = Open(ctx, name); err == nil {
		t.Fatal("second writer opened")
	}
	if _, err = s.BootstrapAccount(ctx, "admin", "short", bootstrapSeedFixture(t)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	a, err := s.BootstrapAccount(ctx, "Admin", "permanent-admin-password", bootstrapSeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BootstrapAccount(ctx, "other", "another-admin-password", bootstrapSeedFixture(t)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	session, token, err := s.Authenticate(ctx, "ADMIN", "permanent-admin-password")
	if err != nil || session.Account.ID != a.ID || a.ID == a.Username {
		t.Fatal(session, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.Login(ctx, token)
	if err != nil || restored.Account.ID != a.ID {
		t.Fatal(restored, err)
	}
	raw, _ := json.Marshal(restored)
	if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), token) {
		t.Fatal("identity exposed credentials")
	}
	// A rolled-back transaction must never create a discoverable identity.
	err = s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.insertAccount(ctx, tx, "partial", "Partial", "user", "not-a-credential", false); err != nil {
			return err
		}
		return ErrConflict
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var count int
	if err = s.reader.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE username='partial'`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func TestTemporaryLoginSingleUseResetAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	user, password, err := createTestAccount(t, s, admin.ID, "User", "Example User", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != 24 {
		t.Fatal("temporary password length", len(password))
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session, token, err := s.Authenticate(ctx, "USER", password)
			if err == nil {
				if !session.Restricted {
					t.Error("temporary session unrestricted")
				}
				results <- token
			} else if !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	tokens := []string{}
	for token := range results {
		tokens = append(tokens, token)
	}
	if len(tokens) != 1 {
		t.Fatal("single-use race", len(tokens))
	}
	token := tokens[0]
	if err = s.RevokeAllLogins(ctx, token); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	replacement, err := s.ResetAccountPassword(ctx, adminTestToken(t, s, admin), user.ID, AccountRevision{RequestID: uuid.NewString(), ExpectedRevision: user.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("reset retained restricted cookie", err)
	}
	if _, _, err = s.Authenticate(ctx, "user", password); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("reset retained old temporary password", err)
	}
	_, token, err = s.Authenticate(ctx, "user", replacement.TemporaryPassword)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, _, err = s.ChangePassword(ctx, token, "", "permanent-user-password", true); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired setup accepted", err)
	}
	s.now = func() time.Time { return now }
	latest, err := s.Account(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.ResetAccountPassword(ctx, adminTestToken(t, s, admin), user.ID, AccountRevision{RequestID: uuid.NewString(), ExpectedRevision: latest.Revision})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(25 * time.Hour) }
	if _, _, err = s.Authenticate(ctx, "user", expired.TemporaryPassword); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired password accepted", err)
	}
}
func TestSetupCompletionAndPermanentChangeAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, admin := accountStore(t)
	_, password, err := createTestAccount(t, s, admin.ID, "user", "User", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Authenticate(ctx, "user", password)
	if err != nil {
		t.Fatal(err)
	}
	_, current, err := s.ChangePassword(ctx, token, "", "permanent-user-password", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ChangePassword(ctx, token, "", "duplicate-user-password", true); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	_, other, err := s.Authenticate(ctx, "user", "permanent-user-password")
	if err != nil {
		t.Fatal(err)
	}
	_, replacement, err := s.ChangePassword(ctx, current, "permanent-user-password", "changed-user-password", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{token, current, other} {
		if _, err = s.Login(ctx, old); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("old session survived password change", err)
		}
	}
	if _, err = s.Login(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAllLogins(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, replacement); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
}
func TestConcurrentLastAdminProtection(t *testing.T) {
	for _, mode := range []string{"demote", "disable"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, first := accountStore(t)
			firstToken := adminTestToken(t, s, first)
			created, err := s.CreateAccount(ctx, firstToken, AccountCreate{RequestID: uuid.NewString(), Username: "second", DisplayName: "Second", Role: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			_, setup, err := s.Authenticate(ctx, "second", created.TemporaryPassword)
			if err != nil {
				t.Fatal(err)
			}
			second, secondToken, err := s.ChangePassword(ctx, setup, "", "second-admin-password", true)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, entry := range []struct {
				account Account
				token   string
			}{{first, firstToken}, {second.Account, secondToken}} {
				wg.Add(1)
				go func(a Account, token string) {
					defer wg.Done()
					<-start
					var err error
					if mode == "disable" {
						_, err = s.SuspendAccount(ctx, token, a.ID, AccountRevision{RequestID: uuid.NewString(), ExpectedRevision: a.Revision})
					} else {
						role := "user"
						_, err = s.UpdateAccount(ctx, token, a.ID, AccountEdit{RequestID: uuid.NewString(), ExpectedRevision: a.Revision, Role: &role})
					}
					results <- err
				}(entry.account, entry.token)
			}
			close(start)
			wg.Wait()
			close(results)
			passed := 0
			for err := range results {
				if err == nil {
					passed++
				} else if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			}
			if passed != 1 {
				t.Fatal("last administrator race", passed)
			}
		})
	}
}
func TestPasswordValidationCountsUnicodeAndBytes(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", 129), string([]byte{0xff}) + strings.Repeat("a", 12)} {
		if ValidatePassword(password) == nil {
			t.Fatal("invalid password accepted")
		}
	}
	for _, password := range []string{strings.Repeat("界", 12), strings.Repeat("😀", 128), " twelve chars "} {
		if err := ValidatePassword(password); err != nil {
			t.Fatal(err)
		}
	}
}

func bootstrapSeedFixture(t *testing.T) BootstrapSeed {
	t.Helper()
	manifest, digest := testfixture.RuntimeManifest()
	network, err := NewNetworkPolicy(Reachability{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := NewBootstrapSeed(ResourceSeed{CPU: 1000, Memory: 1 << 30, Overlay: 256 << 20, Workspace: 1 << 30, Runtime: 1 << 29, MaxMounts: 8, ProjectSlots: 4, AgentSlots: 32, TerminalSlots: 32}, RegisteredSandbox{Reference: ArtifactReference{ID: "test", Version: "v1", Digest: digest}, StoreID: "engine", TemplateID: "agent-runtime", Manifest: manifest}, nil, network.Resolvers)
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

func adminTestToken(t *testing.T, s *Store, a Account) string {
	t.Helper()
	_, token, err := s.Authenticate(ctx, a.Username, "permanent-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func createTestAccount(t *testing.T, s *Store, actor, username, display, role string) (Account, string, error) {
	t.Helper()
	a, err := s.Account(ctx, actor)
	if err != nil {
		return Account{}, "", err
	}
	result, err := s.CreateAccount(ctx, adminTestToken(t, s, a), AccountCreate{RequestID: uuid.NewString(), Username: username, DisplayName: display, Role: role})
	return result.User.Account, result.TemporaryPassword, err
}
