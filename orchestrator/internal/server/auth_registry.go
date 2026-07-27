package server

import (
	"log/slog"
	"os"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/config"
)

// statThrottle bounds how often the registry stats the credentials file on the
// hot request path. requireAuth runs per request; login/me force a fresh check
// so a password change takes effect on the very next login attempt.
const authRegistryStatThrottle = time.Second

// authRegistry holds the login credentials and reloads them from disk when the
// file changes, so adding a user or rotating a password does not require an
// orchestrator restart (a restart kills live sandboxes).
//
// Reload safety is deliberately one-directional: a readable, valid file
// replaces the in-memory set; anything else (read error, parse error, file
// vanished) keeps the previous set and logs. Credentials never silently empty
// out, because an empty set with no shared secret means "no login required" —
// a bad file must not open the gate on a public address.
type authRegistry struct {
	path         string
	sharedSecret bool
	log          *slog.Logger

	mu       sync.RWMutex
	users    map[string]config.AuthUser
	loaded   bool // a file was successfully read at least once
	modTime  time.Time
	size     int64
	lastStat time.Time
}

func newAuthRegistry(cfg config.Config, log *slog.Logger) *authRegistry {
	users := make(map[string]config.AuthUser, len(cfg.AuthUsers))
	for _, user := range cfg.AuthUsers {
		users[user.Username] = user
	}
	r := &authRegistry{
		path:         cfg.AuthUsersPath,
		sharedSecret: cfg.SharedSecret != "",
		log:          log,
		users:        users,
		loaded:       len(users) > 0,
	}
	// Seed the stat baseline from the file config.Load already read, so the
	// first request does not pointlessly re-parse it.
	if info, err := os.Stat(r.path); err == nil {
		r.modTime = info.ModTime()
		r.size = info.Size()
	}
	return r
}

// refresh reloads the credentials file if it changed. When force is false the
// stat is throttled, keeping the per-request path at one RWMutex read.
func (r *authRegistry) refresh(force bool) {
	if r == nil || r.path == "" {
		return
	}
	now := time.Now()
	r.mu.Lock()
	if !force && now.Sub(r.lastStat) < authRegistryStatThrottle {
		r.mu.Unlock()
		return
	}
	r.lastStat = now
	knownMod, knownSize, loaded := r.modTime, r.size, r.loaded
	r.mu.Unlock()

	info, err := os.Stat(r.path)
	if err != nil {
		if loaded {
			// The file was there and is now unreadable. Keep serving the last
			// good credential set rather than falling back to open mode.
			r.log.Error("auth users file unreadable, keeping previously loaded credentials",
				"path", r.path, "error", err)
		}
		return
	}
	if info.ModTime().Equal(knownMod) && info.Size() == knownSize {
		return
	}

	users, err := config.LoadAuthUsersFile(r.path)
	if err != nil {
		r.log.Error("failed to reload auth users, keeping previous credentials",
			"path", r.path, "error", err)
		return
	}
	next := make(map[string]config.AuthUser, len(users))
	for _, user := range users {
		next[user.Username] = user
	}

	r.mu.Lock()
	previous := len(r.users)
	r.users = next
	r.loaded = true
	r.modTime = info.ModTime()
	r.size = info.Size()
	r.mu.Unlock()

	r.log.Info("reloaded auth users", "path", r.path, "users", len(next), "previous_users", previous)
}

// lookup returns the credential for username, reloading first so a hash
// written moments ago is honored.
func (r *authRegistry) lookup(username string) (config.AuthUser, bool) {
	if r == nil {
		return config.AuthUser{}, false
	}
	r.refresh(true)
	r.mu.RLock()
	defer r.mu.RUnlock()
	user, ok := r.users[username]
	return user, ok
}

func (r *authRegistry) hasUsers() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.users) > 0
}

// enabled reports whether credential-based login is required. Adding the first
// user to a previously absent file flips this from false to true without a
// restart, so it is evaluated per request (against a throttled stat) rather
// than frozen at boot.
// A nil registry reports open mode, preserving the semantics of the zero-value
// bool this replaced: server.New always builds one, and test fixtures that
// construct a Server literal without credentials expect no login gate.
func (r *authRegistry) enabled() bool {
	if r == nil {
		return false
	}
	if r.sharedSecret {
		return true
	}
	r.refresh(false)
	return r.hasUsers()
}
