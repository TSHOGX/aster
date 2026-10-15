package product

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const identityCallback = "https://example.com/volumes-studio/auth/callback"
const identityVerifier = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"

func identityCode(t *testing.T, s *Store, token string) string {
	t.Helper()
	challenge, err := pkceChallenge(identityVerifier)
	must(t, err)
	code, err := s.AdmitIdentityCode(context.Background(), token, "studio", identityCallback, challenge)
	must(t, err)
	return code
}
func identityExchange(t *testing.T, s *Store, token string) (IdentityAssertion, string) {
	t.Helper()
	a, g, err := s.ExchangeIdentityCode(context.Background(), "studio", identityCallback, identityCode(t, s, token), identityVerifier)
	must(t, err)
	return a, g
}
func TestIdentityExchangeIsBoundAndSingleUse(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	code := identityCode(t, s, token)
	for _, v := range []struct{ client, callback, code, verifier string }{
		{"other", identityCallback, code, identityVerifier}, {"studio", identityCallback + "/wrong", code, identityVerifier}, {"studio", identityCallback, code, strings.Repeat("x", 43)}, {"studio", identityCallback, code, "short"}, {"studio", identityCallback, "missing", identityVerifier},
	} {
		if _, _, err := s.ExchangeIdentityCode(context.Background(), v.client, v.callback, v.code, v.verifier); !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, grant, err := s.ExchangeIdentityCode(context.Background(), "studio", identityCallback, code, identityVerifier)
			if err == nil {
				successes.Add(1)
				if a.Subject != admin.ID || a.CentralSession == token || grant == token {
					t.Error("incorrect authority projection")
				}
			} else if !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("code used", successes.Load(), "times")
	}
	var rawCount int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM identity_codes WHERE code_digest=?`, code).Scan(&rawCount))
	if rawCount != 0 {
		t.Fatal("raw code persisted")
	}
}
func TestIdentityExpiryAndEvidenceBounds(t *testing.T) {
	s, admin := accountStore(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	token := adminTestToken(t, s, admin)
	code := identityCode(t, s, token)
	now = now.Add(time.Minute)
	if _, _, err := s.ExchangeIdentityCode(context.Background(), "studio", identityCallback, code, identityVerifier); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired code accepted", err)
	}
	a, grant := identityExchange(t, s, token)
	expiry, err := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	must(t, err)
	if expiry.Sub(now) > time.Minute || !expiry.After(now) {
		t.Fatal("unbounded evidence", expiry)
	}
	if _, err := s.IntrospectIdentity(context.Background(), "other", grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-client grant accepted", err)
	}
	if _, err := s.Login(context.Background(), grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("app grant became Aster cookie", err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if _, err := s.IntrospectIdentity(context.Background(), "studio", grant); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired central session accepted", err)
	}
}
func TestIdentityRevocation(t *testing.T) {
	for _, mode := range []string{"logout", "all-devices", "password", "disable", "demote", "derived-logout"} {
		t.Run(mode, func(t *testing.T) {
			s, admin := accountStore(t)
			ctx := context.Background()
			token := adminTestToken(t, s, admin)
			_, grant := identityExchange(t, s, token)
			code := identityCode(t, s, token)
			switch mode {
			case "logout":
				must(t, s.RevokeLogin(ctx, token))
			case "all-devices":
				must(t, s.RevokeAllLogins(ctx, token))
			case "password":
				_, _, err := s.ChangePassword(ctx, token, "permanent-admin-password", "new-permanent-password", false)
				must(t, err)
			case "disable":
				must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE accounts SET status='disabled',session_revision=session_revision+1 WHERE id=?`, admin.ID)
					return err
				}))
			case "demote":
				must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE accounts SET role='user',session_revision=session_revision+1 WHERE id=?`, admin.ID)
					return err
				}))
			case "derived-logout":
				must(t, s.RevokeIdentity(ctx, "studio", grant))
			}
			if _, err := s.IntrospectIdentity(ctx, "studio", grant); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("revocation not observed", err)
			}
			if _, _, err := s.ExchangeIdentityCode(ctx, "studio", identityCallback, code, identityVerifier); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("revoked code exchanged", err)
			}

			authorityErr := s.AuthorizeVolumeImport(ctx, admin.ID)
			if mode == "disable" || mode == "demote" {
				if !errors.Is(authorityErr, ErrUnauthorized) {
					t.Fatal("revoked import authority accepted", authorityErr)
				}
			} else {
				must(t, authorityErr)
			}
		})
	}
}
func TestIdentityAdmissionRequiresCurrentSessionAndBoundedCodes(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	challenge, _ := pkceChallenge(identityVerifier)
	if _, err := s.AdmitIdentityCode(context.Background(), "missing", "studio", identityCallback, challenge); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		identityCode(t, s, token)
	}
	if _, err := s.AdmitIdentityCode(context.Background(), token, "studio", identityCallback, challenge); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded code admission", err)
	}
	if _, err := s.AdmitIdentityCode(context.Background(), token, "studio", identityCallback, "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid challenge accepted", err)
	}
}
func TestIdentityGrantsSurviveRestartWithoutRawCredentials(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "identity.db")
	s, err := Open(ctx, filename)
	must(t, err)
	admin, err := s.BootstrapAccount(ctx, "admin", "permanent-admin-password", bootstrapSeedFixture(t))
	must(t, err)
	token := adminTestToken(t, s, admin)
	before, grant := identityExchange(t, s, token)
	var session string
	must(t, s.reader.QueryRow(`SELECT session_digest FROM identity_grants WHERE grant_digest=?`, tokenDigest(grant)).Scan(&session))
	if session == token || session != before.CentralSession {
		t.Fatal("invalid persisted lineage")
	}
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM identity_grants WHERE grant_digest=?`, grant).Scan(&count))
	if count != 0 {
		t.Fatal("raw derived grant persisted")
	}
	must(t, s.Close())
	s, err = Open(ctx, filename)
	must(t, err)
	defer s.Close()
	after, err := s.IntrospectIdentity(ctx, "studio", grant)
	must(t, err)
	if after.Subject != before.Subject || after.CentralSession != before.CentralSession {
		t.Fatal("identity changed after restart")
	}
}

func TestStudioIdentitySupportsScopedExpertsAndCurrentRevocation(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	adminToken := adminTestToken(t, s, admin)
	user, password, err := createTestAccount(t, s, admin.ID, "expert", "Expert", "user")
	must(t, err)
	_, restricted, err := s.Authenticate(ctx, user.Username, password)
	must(t, err)
	_, token, err := s.ChangePassword(ctx, restricted, "", "expert-permanent-password", true)
	must(t, err)
	challenge, _ := pkceChallenge(identityVerifier)
	if _, err = s.AdmitIdentityCode(ctx, token, "studio", identityCallback, challenge); !errors.Is(err, ErrForbidden) {
		t.Fatal("ungranted entrance", err)
	}
	must(t, grantFixture(s, ctx, adminToken, "account", user.ID, "alpha", "volume.read", "allow", 0))
	assertion, grant := identityExchange(t, s, token)
	if len(assertion.Entitlements) != 1 || assertion.Entitlements[0] != StudioAccessEntitlement {
		t.Fatal("expert obtained library creation", assertion)
	}
	if err = s.AuthorizeVolumeImport(ctx, user.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("Studio access grants import authority", err)
	}
	must(t, grantFixture(s, ctx, adminToken, "account", user.ID, "alpha", "volume.read", "deny", 1))
	if _, err = s.IntrospectIdentity(ctx, "studio", grant); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked read grant retained entrance", err)
	}
	// A different library can retain entrance; it never revives the denied grant.
	must(t, grantFixture(s, ctx, adminToken, "account", user.ID, "beta", "volume.read", "allow", 0))
	_, err = s.IntrospectIdentity(ctx, "studio", grant)
	must(t, err)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO account_memberships(account_id,group_id) VALUES(?,?) ON CONFLICT DO NOTHING", user.ID, StandardUsersGroup)
		return err
	}))
	must(t, grantFixture(s, ctx, adminToken, "group", StandardUsersGroup, "beta", "volume.read", "deny", 0))
	if _, err = s.IntrospectIdentity(ctx, "studio", grant); !errors.Is(err, ErrForbidden) {
		t.Fatal("group deny ignored", err)
	}
	must(t, grantFixture(s, ctx, adminToken, "group", StandardUsersGroup, "gamma", "volume.read", "allow", 0))
	_, err = s.IntrospectIdentity(ctx, "studio", grant)
	must(t, err)
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM account_memberships WHERE account_id=? AND group_id=?", user.ID, StandardUsersGroup)
		return err
	}))
	// beta's direct grant is now effective after leaving the denying group.
	_, err = s.IntrospectIdentity(ctx, "studio", grant)
	must(t, err)
	must(t, grantFixture(s, ctx, adminToken, "account", user.ID, "beta", "volume.read", "deny", 1))
	if _, err = s.IntrospectIdentity(ctx, "studio", grant); !errors.Is(err, ErrForbidden) {
		t.Fatal("removed membership still grants entrance", err)
	}
	must(t, s.RevokeIdentity(ctx, "studio", grant))
	if _, err = s.Login(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked expert could not log out", err)
	}
	adminAssertion, _ := identityExchange(t, s, adminToken)
	if len(adminAssertion.Entitlements) != 2 || adminAssertion.Entitlements[1] != StudioCreateEntitlement {
		t.Fatal(adminAssertion)
	}
}
