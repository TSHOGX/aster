package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/store"
)

// The auth users file holds login credentials only; keep it unreadable to
// anyone but the owner (the orchestrator runs as root).
const authUsersFileMode fs.FileMode = 0o600

const userUsage = `orchestrator user — manage workbench login credentials without a restart

  user list                        list usernames (hashes are never printed)
  user add <name>                  add a user
  user passwd <name>               replace a user's password
  user role <name> <user|admin>    change a user's administrator role
  user rm <name>                   remove a user

Password input (never a flag, so it stays out of shell history):
  --password-stdin                 read the password from stdin
  HARNESS_PASSWORD=...             read the password from the environment

Flags:
  --keep-sessions                  passwd/rm: leave existing login cookies valid
  --force                          rm: allow removing the last user (reopens the
                                   login gate to everyone)

Changes take effect on the next login attempt; the running orchestrator reloads
the file when it changes. No restart, no maintenance page.
`

// runUser dispatches the `orchestrator user` subcommands. Each is a bounded
// one-shot job: it edits the credentials file, optionally revokes login
// sessions, and exits.
// The subcommands write to stdout/stderr rather than the slog logger: this is a
// CLI whose output an operator reads directly.
func runUser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(userUsage)
		return nil
	}
	action := args[0]
	rest := args[1:]

	// flag.Parse stops at the first non-flag argument, so pull the positionals
	// out first. That way `user add alice --password-stdin` works as naturally
	// as `user add --password-stdin alice`.
	flags, positional := splitFlagsAndArgs(rest)
	fset := flag.NewFlagSet("user "+action, flag.ContinueOnError)
	passwordStdin := fset.Bool("password-stdin", false, "read the password from stdin")
	keepSessions := fset.Bool("keep-sessions", false, "leave existing login sessions valid")
	force := fset.Bool("force", false, "allow removing the last remaining user")
	if err := fset.Parse(flags); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch action {
	case "list":
		return userList(ctx, cfg)
	case "add":
		username, err := singleUsername(positional)
		if err != nil {
			return err
		}
		return userAdd(cfg, username, *passwordStdin)
	case "passwd":
		username, err := singleUsername(positional)
		if err != nil {
			return err
		}
		return userPasswd(ctx, cfg, username, *passwordStdin, *keepSessions)
	case "role":
		if len(positional) != 2 {
			return errors.New("expected username and role (user or admin)")
		}
		return userRole(ctx, cfg, positional[0], positional[1])
	case "rm", "remove", "delete":
		username, err := singleUsername(positional)
		if err != nil {
			return err
		}
		return userRemove(ctx, cfg, username, *keepSessions, *force)
	case "-h", "--help", "help":
		fmt.Print(userUsage)
		return nil
	default:
		fmt.Print(userUsage)
		return fmt.Errorf("unknown user subcommand %q", action)
	}
}

// writeAuthUsers replaces the credentials file atomically: a sibling temp file
// at 0600, fsynced, then renamed over the target. A crash mid-write therefore
// leaves the previous file intact rather than a half-written one the server
// would refuse to parse.
func writeAuthUsers(path string, users []config.AuthUser) error {
	doc := struct {
		Users []config.AuthUser `yaml:"users"`
	}{Users: users}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode auth users: %w", err)
	}
	header := "# Workbench login credentials. Managed by `orchestrator user` /\n" +
		"# `orchestrator user`; edits are picked up without a restart.\n" +
		"# Passwords are stored ONLY as PBKDF2-SHA256 hashes.\n"
	content := append([]byte(header), body...)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".auth-users-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(authUsersFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func singleUsername(positional []string) (string, error) {
	if len(positional) != 1 {
		return "", errors.New("expected exactly one username")
	}
	username := strings.TrimSpace(positional[0])
	if username == "" {
		return "", errors.New("username must not be empty")
	}
	return username, nil
}

// splitFlagsAndArgs partitions args into "-"-prefixed flags and positionals.
// All user subcommand flags are booleans, so no flag consumes a following
// value and a simple prefix split is unambiguous.
func splitFlagsAndArgs(args []string) (flags, positional []string) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			continue
		}
		positional = append(positional, arg)
	}
	return flags, positional
}

func indexOfUser(users []config.AuthUser, username string) int {
	for i, user := range users {
		if user.Username == username {
			return i
		}
	}
	return -1
}

// readPassword takes the password from stdin or HARNESS_PASSWORD. There is
// deliberately no plaintext flag: flags land in shell history and `ps` output.
// Callers that need a hidden prompt (a local operator wrapper) read it with `read -s` and
// pipe it in.
func readPassword(fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password := strings.TrimRight(line, "\r\n")
		if password == "" {
			return "", errors.New("empty password on stdin")
		}
		return password, nil
	}
	if password := os.Getenv("HARNESS_PASSWORD"); password != "" {
		return password, nil
	}
	return "", errors.New("no password provided: pass --password-stdin or set HARNESS_PASSWORD")
}

// revokeSessions invalidates a user's existing login cookies. It opens the
// database without running migrations (the server owns the schema) and is
// best-effort by design: the credentials file has already been updated at this
// point, so a DB problem must be reported without failing the whole operation.
// A cookie that escapes revocation still expires on its own.
func revokeSessions(ctx context.Context, dbPath, username string) {
	db, err := store.OpenForMaintenance(ctx, dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not open %s to revoke sessions: %v\n", dbPath, err)
		fmt.Fprintf(os.Stderr, "         existing cookies for %q stay valid until they expire.\n", username)
		return
	}
	defer db.Close()
	if err := db.RevokeAuthSessionsForUser(ctx, username, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to revoke sessions for %q: %v\n", username, err)
		return
	}
	fmt.Printf("revoked existing login sessions for %q\n", username)
}

// revokeShares revokes every share touching a username: grants to them, grants
// they made, and public links they created.
//
// Unlike revokeSessions this is NOT best-effort, and the difference is the point.
// Usernames are reusable and carry no stable id, so a share left live is
// inherited whole by the next person created under the same name -- and a share,
// unlike a cookie, has no expiry to eventually close the hole. One silent
// failure here is permanent unauthorized read access, so any failure is fatal
// and the caller exits non-zero.
//
// OpenForMaintenance deliberately skips migrate(), so a database predating the
// share tables reports "no such table". That is NOT treated as "no shares to
// revoke": the tables are missing because this binary has not run its schema
// setup against that file, and guessing would be the same silent failure. It is
// reported as an error for an operator to resolve.
// The one exception is a database file that does not exist at all: the
// orchestrator has never run against this path, so there is no share table and
// no share rows either. That is unambiguous, unlike a database that exists but
// lacks the tables.
func revokeShares(ctx context.Context, dbPath, username string) error {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("no database at %s yet; no shares to revoke for %q\n", dbPath, username)
		return nil
	}
	db, err := store.OpenForMaintenance(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("could not open %s to revoke shares for %q: %w", dbPath, username, err)
	}
	defer db.Close()
	revoked, err := db.RevokeSharesForUser(ctx, username, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to revoke shares for %q (a share does not expire on its own; "+
			"resolve this before recreating a user with that name): %w", username, err)
	}
	fmt.Printf("revoked %d share(s) referencing %q\n", revoked, username)
	return nil
}

func userList(ctx context.Context, cfg config.Config) error {
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		return err
	}
	fmt.Printf("credentials file: %s\n", cfg.AuthUsersPath)
	if len(users) == 0 {
		fmt.Println("no users configured — the login gate is OPEN (every request is allowed as \"lab\")")
		return nil
	}
	counts := sessionCountsByUser(ctx, cfg.DBPath)
	sorted := append([]config.AuthUser(nil), users...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Username < sorted[j].Username })
	fmt.Printf("%-24s %-10s %s\n", "USERNAME", "SESSIONS", "HASH")
	for _, user := range sorted {
		sessions := "-"
		if counts != nil {
			sessions = fmt.Sprintf("%d", counts[user.Username])
		}
		fmt.Printf("%-24s %-10s %s\n", user.Username, sessions, hashSummary(user.PasswordHash))
	}
	return nil
}

// hashSummary describes a stored hash without revealing it.
func hashSummary(hash string) string {
	parts := strings.Split(hash, "$")
	if len(parts) < 2 {
		return "(unrecognized)"
	}
	return parts[0] + ", " + parts[1] + " iterations"
}

// sessionCountsByUser returns per-user workbench session counts, or nil when
// the database cannot be read (the counts are informational only).
func sessionCountsByUser(ctx context.Context, dbPath string) map[string]int {
	db, err := store.OpenForMaintenance(ctx, dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()
	userIDs, err := db.DistinctSessionUserIDs(ctx)
	if err != nil {
		return nil
	}
	counts := make(map[string]int, len(userIDs))
	for _, userID := range userIDs {
		sessions, err := db.ListSessionsByUser(ctx, userID)
		if err != nil {
			return nil
		}
		counts[userID] = len(sessions)
	}
	return counts
}

func userAdd(cfg config.Config, username string, passwordStdin bool) error {
	if err := config.ValidateAuthUsername(username); err != nil {
		return err
	}
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		return err
	}
	if indexOfUser(users, username) >= 0 {
		return fmt.Errorf("user %q already exists (use `user passwd %s` to change the password)", username, username)
	}
	password, err := readPassword(passwordStdin)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	firstUser := len(users) == 0
	users = append(users, config.AuthUser{Username: username, PasswordHash: hash})
	if err := writeAuthUsers(cfg.AuthUsersPath, users); err != nil {
		return err
	}
	fmt.Printf("added user %q to %s\n", username, cfg.AuthUsersPath)

	// No `users` table row is created on purpose: user identity lives in
	// sessions.user_id (there is no FK), the table holds only the built-in "lab"
	// account, and the ops queries deliberately never read it. See
	// scripts/README.md. So `add` touches nothing but the credentials file.

	if firstUser {
		fmt.Println("note: this is the first configured user, so the login gate is now ENABLED.")
		fmt.Println("      requests without a valid cookie are rejected from here on.")
	}
	fmt.Println("effective immediately — no restart needed.")
	return nil
}

func userPasswd(ctx context.Context, cfg config.Config, username string, passwordStdin, keepSessions bool) error {
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		return err
	}
	index := indexOfUser(users, username)
	if index < 0 {
		return fmt.Errorf("user %q not found in %s", username, cfg.AuthUsersPath)
	}
	password, err := readPassword(passwordStdin)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	users[index].PasswordHash = hash
	if err := writeAuthUsers(cfg.AuthUsersPath, users); err != nil {
		return err
	}
	fmt.Printf("updated password for %q in %s\n", username, cfg.AuthUsersPath)
	if !keepSessions {
		revokeSessions(ctx, cfg.DBPath, username)
	}
	fmt.Println("effective immediately — no restart needed.")
	return nil
}

func userRole(ctx context.Context, cfg config.Config, username, role string) error {
	if err := config.ValidateAuthUsername(username); err != nil {
		return err
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "user" && role != "admin" {
		return errors.New("role must be user or admin")
	}
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		return err
	}
	index := indexOfUser(users, username)
	if index < 0 {
		return fmt.Errorf("user %q not found in %s", username, cfg.AuthUsersPath)
	}
	previous := users[index].Role
	if previous == "" {
		previous = "user"
	}
	users[index].Role = role
	if err := writeAuthUsers(cfg.AuthUsersPath, users); err != nil {
		return err
	}
	fmt.Printf("updated role for %q: %s -> %s\n", username, previous, role)
	// Role changes take effect on the next request because the registry reloads
	// the file. Existing cookies remain valid; authorization is checked per
	// request, so demotion is immediate without revoking unrelated sessions.
	_ = ctx
	fmt.Println("effective immediately — no restart needed.")
	return nil
}

func userRemove(ctx context.Context, cfg config.Config, username string, keepSessions, force bool) error {
	users, err := config.LoadAuthUsersFile(cfg.AuthUsersPath)
	if err != nil {
		return err
	}
	index := indexOfUser(users, username)
	if index < 0 {
		return fmt.Errorf("user %q not found in %s", username, cfg.AuthUsersPath)
	}
	// Removing the last user empties the file, which means "no login required".
	// That is a real posture change on a public address, so require --force.
	if len(users) == 1 && !force {
		return fmt.Errorf("%q is the only user; removing it would DISABLE the login gate entirely (pass --force if that is intended)", username)
	}
	users = append(users[:index], users[index+1:]...)
	if err := writeAuthUsers(cfg.AuthUsersPath, users); err != nil {
		return err
	}
	fmt.Printf("removed user %q from %s\n", username, cfg.AuthUsersPath)
	if !keepSessions {
		revokeSessions(ctx, cfg.DBPath, username)
	}
	// Share revocation is NOT gated on --keep-sessions. That flag means "leave
	// existing login cookies valid", which is a convenience; leaving shares live
	// is the username-reuse hole itself, and a share has no expiry to close it.
	// `passwd` deliberately does not do this: the same person changing their own
	// password should not lose their grants.
	if err := revokeShares(ctx, cfg.DBPath, username); err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("WARNING: no users remain — the login gate is now OPEN to everyone.")
	}
	fmt.Printf("note: %q's existing sessions and data remain in the database; this only removes the login credential.\n", username)
	fmt.Println("effective immediately — no restart needed.")
	return nil
}
