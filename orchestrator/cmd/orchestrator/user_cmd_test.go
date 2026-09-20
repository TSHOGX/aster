package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/store"
)

func TestWriteAuthUsersRoundTripsAndIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-users.yaml")
	want := []config.AuthUser{
		{Username: "alice", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"},
		{Username: "bob-2_x", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$b3RoZXI"},
	}
	if err := writeAuthUsers(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials file must be 0600, got %o", perm)
	}

	// The server's own loader must accept what we wrote.
	got, err := config.LoadAuthUsersFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d users, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("user %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// An empty user list must still round-trip: `user rm --force` on the last user
// writes one, and the server has to read it as "no users" rather than error.
func TestWriteAuthUsersEmptyListRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-users.yaml")
	if err := writeAuthUsers(path, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	users, err := config.LoadAuthUsersFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("want no users, got %d", len(users))
	}
}

// Overwriting must not leave temp files behind in the credentials directory.
func TestWriteAuthUsersLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-users.yaml")
	for i := 0; i < 3; i++ {
		if err := writeAuthUsers(path, []config.AuthUser{{Username: "alice", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"}}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth-users.yaml" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected only auth-users.yaml, got %v", names)
	}
}

func TestSplitFlagsAndArgs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantFlags  []string
		wantPosArg []string
	}{
		{"flag after name", []string{"alice", "--password-stdin"}, []string{"--password-stdin"}, []string{"alice"}},
		{"flag before name", []string{"--password-stdin", "alice"}, []string{"--password-stdin"}, []string{"alice"}},
		{"name only", []string{"alice"}, nil, []string{"alice"}},
		{"multiple flags", []string{"alice", "--keep-sessions", "--force"}, []string{"--keep-sessions", "--force"}, []string{"alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags, positional := splitFlagsAndArgs(tc.args)
			if len(flags) != len(tc.wantFlags) {
				t.Fatalf("flags: want %v got %v", tc.wantFlags, flags)
			}
			for i := range tc.wantFlags {
				if flags[i] != tc.wantFlags[i] {
					t.Fatalf("flags: want %v got %v", tc.wantFlags, flags)
				}
			}
			if len(positional) != len(tc.wantPosArg) {
				t.Fatalf("positional: want %v got %v", tc.wantPosArg, positional)
			}
			for i := range tc.wantPosArg {
				if positional[i] != tc.wantPosArg[i] {
					t.Fatalf("positional: want %v got %v", tc.wantPosArg, positional)
				}
			}
		})
	}
}

func TestReadPasswordRejectsMissingInput(t *testing.T) {
	t.Setenv("HARNESS_PASSWORD", "")
	if _, err := readPassword(false); err == nil {
		t.Fatalf("expected an error when no password source is available")
	}
	t.Setenv("HARNESS_PASSWORD", "from-env")
	got, err := readPassword(false)
	if err != nil {
		t.Fatalf("env password: %v", err)
	}
	if got != "from-env" {
		t.Fatalf("want %q, got %q", "from-env", got)
	}
}

func TestHashSummaryDoesNotLeakHash(t *testing.T) {
	hash := "pbkdf2-sha256$600000$c2FsdA$c2VjcmV0LWRpZ2VzdA"
	summary := hashSummary(hash)
	if want := "pbkdf2-sha256, 600000 iterations"; summary != want {
		t.Fatalf("want %q, got %q", want, summary)
	}
	if summary == hash {
		t.Fatalf("summary must not be the hash itself")
	}
}

// passwd and rm must invalidate the user's existing cookies, otherwise a
// removed account keeps working until its session TTL expires.
func TestUserPasswdAndRemoveRevokeSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state", "orchestrator.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Config{
		AuthUsersPath: filepath.Join(dir, "auth-users.yaml"),
		DBPath:        dbPath,
	}
	if err := writeAuthUsers(cfg.AuthUsersPath, []config.AuthUser{
		{Username: "alice", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"},
		{Username: "bob", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$b3RoZXI"},
	}); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	// Two live login sessions, one per user.
	now := time.Now().UTC()
	for _, user := range []string{"alice", "bob"} {
		if err := db.CreateAuthSession(ctx, store.AuthSession{
			TokenHash: "token-" + user,
			UserID:    user,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("create session for %s: %v", user, err)
		}
	}
	assertSession := func(user string, wantValid bool) {
		t.Helper()
		_, err := db.LookupAuthSession(ctx, "token-"+user, time.Now().UTC())
		if wantValid && err != nil {
			t.Fatalf("%s's session should still be valid, got %v", user, err)
		}
		if !wantValid && err == nil {
			t.Fatalf("%s's session should have been revoked", user)
		}
	}

	t.Setenv("HARNESS_PASSWORD", "rotated-pw")
	if err := userPasswd(ctx, cfg, "alice", false, false); err != nil {
		t.Fatalf("passwd: %v", err)
	}
	assertSession("alice", false)
	assertSession("bob", true)

	if err := userRemove(ctx, cfg, "bob", false, false); err != nil {
		t.Fatalf("rm: %v", err)
	}
	assertSession("bob", false)

	// The file now reflects both operations.
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" {
		t.Fatalf("want only alice remaining, got %+v", users)
	}
	if users[0].PasswordHash == "pbkdf2-sha256$600000$c2FsdA$aGFzaA" {
		t.Fatalf("alice's hash should have been replaced")
	}
}

// Username reuse: `rm` must revoke every share touching the name, and `passwd`
// must not (same person, new credential). --keep-sessions covers cookies only;
// a share has no expiry, so it is revoked regardless.
func TestUserRemoveRevokesSharesAndPasswdDoesNot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state", "orchestrator.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Config{AuthUsersPath: filepath.Join(dir, "auth-users.yaml"), DBPath: dbPath}
	if err := writeAuthUsers(cfg.AuthUsersPath, []config.AuthUser{
		{Username: "alice", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"},
		{Username: "bob", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$b3RoZXI"},
	}); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	now := time.Now().UTC()
	session := store.Session{
		ID: "sess_1", UserID: "alice", Status: "created", Agent: "pi", Mode: "agent",
		Workspace: filepath.Join(dir, "sess_1"), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := db.CreateSessionShare(ctx, store.SessionShare{
		ID: "share_1", SessionID: "sess_1", GranteeUserID: "bob", Role: store.ShareRoleReader,
		Visibility: store.ShareVisibilityFull, CreatedBy: "alice", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create share: %v", err)
	}
	shareLive := func() bool {
		t.Helper()
		_, ok, err := db.ResolveSessionShare(ctx, "sess_1", "bob")
		if err != nil {
			t.Fatalf("resolve share: %v", err)
		}
		return ok
	}

	// passwd on the grantee leaves the grant alone.
	t.Setenv("HARNESS_PASSWORD", "rotated-pw")
	if err := userPasswd(ctx, cfg, "bob", false, false); err != nil {
		t.Fatalf("passwd: %v", err)
	}
	if !shareLive() {
		t.Fatal("passwd must not revoke shares")
	}

	// rm revokes it, even with --keep-sessions.
	if err := userRemove(ctx, cfg, "bob", true, false); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if shareLive() {
		t.Fatal("rm must revoke the removed user's shares")
	}
}

// A database that exists but has no share tables must be a hard error, not
// silently read as "no shares to revoke". OpenForMaintenance skips migrate(), so
// this is reachable in production against an un-migrated file.
func TestRevokeSharesFailsOnMissingShareTables(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "orchestrator.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE placeholder (id TEXT)`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	if err := revokeShares(ctx, dbPath, "bob"); err == nil {
		t.Fatal("expected an error when the share tables are absent")
	}
}

// A database file that does not exist at all is unambiguous: there are no shares.
func TestRevokeSharesSucceedsWhenDatabaseAbsent(t *testing.T) {
	if err := revokeShares(context.Background(), filepath.Join(t.TempDir(), "missing.db"), "bob"); err != nil {
		t.Fatalf("absent database should not be an error, got %v", err)
	}
}

// --keep-sessions is the documented escape hatch for rotating a password
// without logging the user out everywhere.
func TestUserPasswdKeepSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state", "orchestrator.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Config{AuthUsersPath: filepath.Join(dir, "auth-users.yaml"), DBPath: dbPath}
	if err := writeAuthUsers(cfg.AuthUsersPath, []config.AuthUser{
		{Username: "alice", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"},
	}); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	now := time.Now().UTC()
	if err := db.CreateAuthSession(ctx, store.AuthSession{
		TokenHash: "token-alice", UserID: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	t.Setenv("HARNESS_PASSWORD", "rotated-pw")
	if err := userPasswd(ctx, cfg, "alice", false, true); err != nil {
		t.Fatalf("passwd: %v", err)
	}
	if _, err := db.LookupAuthSession(ctx, "token-alice", time.Now().UTC()); err != nil {
		t.Fatalf("--keep-sessions must leave the cookie valid, got %v", err)
	}
}

// Removing the last user would reopen the gate, so it needs --force.
func TestUserRemoveLastUserRequiresForce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Config{
		AuthUsersPath: filepath.Join(dir, "auth-users.yaml"),
		DBPath:        filepath.Join(dir, "state", "orchestrator.db"),
	}
	if err := writeAuthUsers(cfg.AuthUsersPath, []config.AuthUser{
		{Username: "alice", PasswordHash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"},
	}); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	if err := userRemove(ctx, cfg, "alice", true, false); err == nil {
		t.Fatalf("removing the last user without --force must fail")
	}
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil || len(users) != 1 {
		t.Fatalf("refused removal must leave the file untouched (err=%v users=%+v)", err, users)
	}

	if err := userRemove(ctx, cfg, "alice", true, true); err != nil {
		t.Fatalf("--force removal: %v", err)
	}
	users, err = config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("want no users after forced removal, got %+v", users)
	}
}

// Adding a duplicate must fail before touching the file.
func TestUserAddRejectsDuplicateAndBadName(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		AuthUsersPath: filepath.Join(dir, "auth-users.yaml"),
		DBPath:        filepath.Join(dir, "state", "orchestrator.db"),
	}
	t.Setenv("HARNESS_PASSWORD", "pw")
	if err := userAdd(cfg, "alice", false); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := userAdd(cfg, "alice", false); err == nil {
		t.Fatalf("duplicate add must fail")
	}
	if err := userAdd(cfg, "bad name!", false); err == nil {
		t.Fatalf("invalid username must fail")
	}
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" {
		t.Fatalf("rejected adds must not modify the file, got %+v", users)
	}
}

func TestIndexOfUser(t *testing.T) {
	users := []config.AuthUser{{Username: "alice"}, {Username: "bob"}}
	if got := indexOfUser(users, "bob"); got != 1 {
		t.Fatalf("bob: want 1 got %d", got)
	}
	if got := indexOfUser(users, "carol"); got != -1 {
		t.Fatalf("carol: want -1 got %d", got)
	}
}
