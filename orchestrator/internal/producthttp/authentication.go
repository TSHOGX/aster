package producthttp

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/product"
)

const loginCookie = "project_login"

type AuthUser struct {
	Username     string `json:"username" yaml:"username"`
	PasswordHash string `json:"-" yaml:"password_hash"`
	Role         string `json:"role" yaml:"role"`
}
type Authentication struct {
	Store            *product.Store
	Config           Config
	DevelopmentActor string
	mu               sync.RWMutex
	users            map[string]AuthUser
	attempts         *auth.RateLimiter
	requests         *auth.IPRateLimiter
}

func NewAuthentication(store *product.Store, config Config, users []AuthUser, developmentActor string) (*Authentication, error) {
	a := &Authentication{Store: store, Config: config, DevelopmentActor: developmentActor, attempts: auth.NewRateLimiter(5, 15*time.Minute), requests: auth.NewIPRateLimiter(2, 10)}
	if len(users) == 0 && developmentActor == "" {
		return nil, fmt.Errorf("credentials or explicit development actor required")
	}
	if len(users) > 0 && developmentActor != "" {
		return nil, fmt.Errorf("credentials and development actor are mutually exclusive")
	}
	if err := a.ReplaceUsers(users); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *Authentication) ReplaceUsers(users []AuthUser) error {
	updated := map[string]AuthUser{}
	for _, user := range users {
		if len(user.Username) < 1 || len(user.Username) > 64 || !auth.ValidPasswordHash(user.PasswordHash) {
			return fmt.Errorf("valid username and PBKDF2 hash required")
		}
		for _, c := range user.Username {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return fmt.Errorf("invalid username")
			}
		}
		if _, exists := updated[user.Username]; exists {
			return fmt.Errorf("duplicate username")
		}
		if user.Role == "" {
			user.Role = "user"
		}
		if user.Role != "user" && user.Role != "admin" {
			return fmt.Errorf("invalid role")
		}
		updated[user.Username] = user
	}
	a.mu.Lock()
	a.users = updated
	a.mu.Unlock()
	return nil
}
func credentialDigest(hash string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(hash))) }
func (a *Authentication) Actor(r *http.Request) (string, error) {
	if a.DevelopmentActor != "" {
		return a.DevelopmentActor, nil
	}
	cookie, err := r.Cookie(loginCookie)
	if err != nil {
		return "", product.ErrNotFound
	}
	actor, digest, err := a.Store.Login(r.Context(), cookie.Value)
	if err != nil {
		return "", err
	}
	a.mu.RLock()
	user, exists := a.users[actor]
	a.mu.RUnlock()
	if !exists || credentialDigest(user.PasswordHash) != digest {
		return "", product.ErrNotFound
	}
	return actor, nil
}
func (a *Authentication) Handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/session", a.session)
	mux.HandleFunc("GET /api/users", a.listUsers)
	mux.Handle("/", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") && (r.Method != "GET" && !a.Config.originAllowed(r)) {
			writeError(w, 403, "cross-origin request refused")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *Authentication) login(w http.ResponseWriter, r *http.Request) {
	if a.DevelopmentActor != "" {
		a.session(w, r)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if a.Config.TrustForwardedHost {
		ip = r.Header.Get("X-Real-IP")
	}
	if !a.requests.Allowed(ip) {
		writeError(w, 429, "too many login attempts")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, 16<<10, &body) {
		return
	}
	key := ip + "/" + body.Username
	if !a.attempts.Allowed(key) {
		writeError(w, 429, "too many login attempts")
		return
	}
	a.mu.RLock()
	user, exists := a.users[body.Username]
	dummy := ""
	if !exists {
		for _, candidate := range a.users {
			dummy = candidate.PasswordHash
			break
		}
	}
	a.mu.RUnlock()
	encoded := user.PasswordHash
	if !exists {
		encoded = dummy
	}
	valid := auth.VerifyPassword(encoded, body.Password)
	if !exists || !valid {
		a.attempts.RecordFailure(key)
		writeError(w, 401, "invalid credentials")
		return
	}
	a.attempts.RecordSuccess(key)
	token, err := a.Store.CreateLogin(r.Context(), user.Username, credentialDigest(user.PasswordHash), 7*24*time.Hour)
	if err != nil {
		fail(w, err)
		return
	}
	a.cookie(w, r, token, 7*24*3600)
	writeJSON(w, 200, map[string]any{"username": user.Username, "role": user.Role})
}
func (a *Authentication) cookie(w http.ResponseWriter, r *http.Request, value string, age int) {
	secure := r.TLS != nil || a.Config.TrustForwardedHost && r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}
func (a *Authentication) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(loginCookie); err == nil {
		if err = a.Store.RevokeLogin(r.Context(), cookie.Value); err != nil {
			fail(w, err)
			return
		}
	}
	a.cookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func (a *Authentication) session(w http.ResponseWriter, r *http.Request) {
	actor, err := a.Actor(r)
	if err != nil {
		writeError(w, 401, "login required")
		return
	}
	a.mu.RLock()
	user := a.users[actor]
	a.mu.RUnlock()
	role := user.Role
	if role == "" {
		role = "user"
	}
	writeJSON(w, 200, map[string]any{"username": actor, "role": role, "development": a.DevelopmentActor != ""})
}
func (a *Authentication) listUsers(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Actor(r); err != nil {
		writeError(w, 401, "login required")
		return
	}
	a.mu.RLock()
	users := make([]string, 0, len(a.users))
	for name := range a.users {
		users = append(users, name)
	}
	a.mu.RUnlock()
	sort.Strings(users)
	writeJSON(w, 200, map[string]any{"users": users})
}
func (a *Authentication) GC() { a.attempts.GC(); a.requests.GC() }
