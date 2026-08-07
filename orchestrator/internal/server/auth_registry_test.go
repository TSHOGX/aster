package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/store"
)

// writeUsersFile writes a credentials file, mirroring what `orchestrator user`
// does. It deliberately does not touch mtime: a rotation in the field lands in
// the same timestamp tick with an identical file size, and the registry must
// detect it from content alone.
func writeUsersFile(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	body := "users:\n"
	for username, password := range entries {
		hash, err := auth.HashPasswordWithIterations(password, 1000)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		body += fmt.Sprintf("  - username: %s\n    password_hash: %q\n", username, hash)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write users file: %v", err)
	}
}

func newRegistry(t *testing.T, path string, users []config.AuthUser) *authRegistry {
	t.Helper()
	return newAuthRegistry(config.Config{AuthUsersPath: path, AuthUsers: users}, slog.Default())
}

func TestAuthRegistryPicksUpNewUserWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-users.yaml")
	writeUsersFile(t, path, map[string]string{"alice": "alice-pw"})
	users, err := config.LoadAuthUsersFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reg := newRegistry(t, path, users)

	if _, ok := reg.lookup("bob"); ok {
		t.Fatalf("bob should not exist yet")
	}

	// Simulate `orchestrator user add bob`.
	writeUsersFile(t, path, map[string]string{"alice": "alice-pw", "bob": "bob-pw"})

	bob, ok := reg.lookup("bob")
	if !ok {
		t.Fatalf("bob should be visible after the file changed, without a restart")
	}
	if !auth.VerifyPassword(bob.PasswordHash, "bob-pw") {
		t.Fatalf("reloaded hash does not verify bob's password")
	}
	if _, ok := reg.lookup("alice"); !ok {
		t.Fatalf("alice should still exist after reload")
	}
}

func TestAuthRegistryPicksUpRotatedPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-users.yaml")
	writeUsersFile(t, path, map[string]string{"alice": "old-pw"})
	users, err := config.LoadAuthUsersFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reg := newRegistry(t, path, users)

	writeUsersFile(t, path, map[string]string{"alice": "new-pw"})

	alice, ok := reg.lookup("alice")
	if !ok {
		t.Fatalf("alice missing after reload")
	}
	if auth.VerifyPassword(alice.PasswordHash, "old-pw") {
		t.Fatalf("old password must stop working after rotation")
	}
	if !auth.VerifyPassword(alice.PasswordHash, "new-pw") {
		t.Fatalf("new password must work after rotation")
	}
}

// A malformed or deleted file must never empty the credential set, because an
// empty set with no shared secret means "no login required".
func TestAuthRegistryKeepsCredentialsWhenFileBreaks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{"malformed yaml", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("users:\n  - username: [not-a-string\n"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{"invalid hash", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("users:\n  - username: alice\n    password_hash: \"plaintext\"\n"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}},
		{"file removed", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth-users.yaml")
			writeUsersFile(t, path, map[string]string{"alice": "alice-pw"})
			users, err := config.LoadAuthUsersFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			reg := newRegistry(t, path, users)

			tc.damage(t, path)

			alice, ok := reg.lookup("alice")
			if !ok {
				t.Fatalf("credentials must survive a broken file")
			}
			if !auth.VerifyPassword(alice.PasswordHash, "alice-pw") {
				t.Fatalf("surviving hash must still verify")
			}
			if !reg.enabled() {
				t.Fatalf("login gate must stay enabled when the file breaks")
			}
		})
	}
}

// Adding the first user to a previously absent file flips the gate from open to
// enabled without a restart.
func TestAuthRegistryEnablesGateOnFirstUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-users.yaml")
	reg := newRegistry(t, path, nil)
	if reg.enabled() {
		t.Fatalf("no file and no users: gate should be open")
	}

	writeUsersFile(t, path, map[string]string{"alice": "alice-pw"})
	reg.refresh(true)

	if !reg.enabled() {
		t.Fatalf("gate should be enabled once the first user exists")
	}
	if _, ok := reg.lookup("alice"); !ok {
		t.Fatalf("alice should be loadable")
	}
}

// End to end at the HTTP layer: rotating the password on disk changes what
// POST /api/login accepts, with no server restart in between.
func TestLoginHonorsRotatedPasswordWithoutRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	path := filepath.Join(dir, "auth-users.yaml")
	writeUsersFile(t, path, map[string]string{"lab": "old-pw"})
	users, err := config.LoadAuthUsersFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	srv := newAuthTestServer(t, dir, st, users)
	srv.creds = newRegistry(t, path, users)

	login := func(password string) int {
		rec := httptest.NewRecorder()
		body := fmt.Sprintf(`{"username":"lab","password":%q}`, password)
		srv.login(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body)))
		return rec.Code
	}

	if code := login("old-pw"); code != http.StatusOK {
		t.Fatalf("old password before rotation: want 200 got %d", code)
	}

	// Simulate `orchestrator user passwd lab`.
	writeUsersFile(t, path, map[string]string{"lab": "new-pw"})

	if code := login("old-pw"); code != http.StatusUnauthorized {
		t.Fatalf("old password after rotation: want 401 got %d", code)
	}
	if code := login("new-pw"); code != http.StatusOK {
		t.Fatalf("new password after rotation: want 200 got %d", code)
	}
}

// With a shared secret the gate is enabled regardless of the file.
func TestAuthRegistrySharedSecretAlwaysEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-users.yaml")
	reg := newAuthRegistry(config.Config{AuthUsersPath: path, SharedSecret: "shh"}, slog.Default())
	if !reg.enabled() {
		t.Fatalf("shared secret must enable the gate")
	}
}
