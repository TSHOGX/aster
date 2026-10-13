package producthttp

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/product"
)

const loginCookie = "project_login"

type Authentication struct {
	VolumeImports    http.Handler
	ProjectInspector product.ProjectInspector
	RuntimeInspector product.RuntimeInspector
	Store            *product.Store
	Config           Config
	attempts         *auth.RateLimiter
	requests         *auth.IPRateLimiter
}

func NewAuthentication(store *product.Store, config Config) *Authentication {
	return &Authentication{Store: store, Config: config, attempts: auth.NewRateLimiter(5, 15*time.Minute), requests: auth.NewIPRateLimiter(2, 10)}
}
func requestToken(r *http.Request) string {
	cookie, err := r.Cookie(loginCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}
func (a *Authentication) Actor(r *http.Request) (string, error) {
	session, err := a.Store.Login(r.Context(), requestToken(r))
	if err != nil {
		return "", err
	}
	if session.Restricted {
		return "", product.ErrUnauthorized
	}
	return session.Account.ID, nil
}
func (a *Authentication) Handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	a.adminRoutes(mux)
	a.identityRoutes(mux)
	if a.VolumeImports != nil {
		mux.Handle("/api/admin/volume-sources", a.VolumeImports)
		mux.Handle("/api/admin/volume-sources/", a.VolumeImports)
		mux.Handle("/api/admin/volume-imports", a.VolumeImports)
		mux.Handle("/api/admin/volume-imports/", a.VolumeImports)
	}
	a.groupRoutes(mux)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/session", a.session)
	mux.HandleFunc("POST /api/auth/password/setup", a.password)
	mux.HandleFunc("POST /api/me/password", a.password)
	mux.HandleFunc("DELETE /api/me/sessions", a.logoutAll)
	mux.HandleFunc("GET /api/users", a.listUsers)
	a.resourceRoutes(mux)
	mux.HandleFunc("GET /api/me/preferences", a.preferences)
	mux.HandleFunc("PATCH /api/me/preferences", a.preferences)
	mux.HandleFunc("GET /api/me/profile", a.profile)
	mux.HandleFunc("PATCH /api/me/profile", a.profile)
	mux.Handle("/", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (strings.HasPrefix(r.URL.Path, "/api/auth/") || strings.HasPrefix(r.URL.Path, "/api/me/") || strings.HasPrefix(r.URL.Path, "/api/admin/")) && r.Method != "GET" && !a.Config.originAllowed(r) {
			writeError(w, 403, "cross-origin request refused")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func authFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, product.ErrUnauthorized) {
		writeJSON(w, 401, map[string]string{"code": "login_required", "error": "invalid or expired credentials"})
		return
	}
	if errors.Is(err, product.ErrForbidden) {
		writeJSON(w, 403, map[string]string{"code": "action_denied", "error": "action denied"})
		return
	}
	if errors.Is(err, product.ErrInvalid) {
		var admission *product.AdmissionError
		if errors.As(err, &admission) && admission.Code == "current_password_invalid" {
			writeJSON(w, 422, map[string]string{"code": "current_password_invalid", "error": "current password is incorrect"})
			return
		}
		writeJSON(w, 422, map[string]string{"code": "password_invalid", "error": "password must contain 12 to 128 characters and at most 512 UTF-8 bytes"})
		return
	}
	fail(w, err)
}
func (a *Authentication) login(w http.ResponseWriter, r *http.Request) {
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
	key := ip + "/" + strings.ToLower(body.Username)
	if !a.attempts.Allowed(key) {
		writeError(w, 429, "too many login attempts")
		return
	}
	session, token, err := a.Store.Authenticate(r.Context(), body.Username, body.Password)
	if err != nil {
		a.attempts.RecordFailure(key)
		authFailure(w, err)
		return
	}
	a.attempts.RecordSuccess(key)
	a.sessionCookie(w, r, token, session.Restricted)
	writeJSON(w, 200, session.Account)
}
func (a *Authentication) sessionCookie(w http.ResponseWriter, r *http.Request, value string, restricted bool) {
	age := 7 * 24 * 3600
	if restricted {
		age = 15 * 60
	}
	a.cookie(w, r, value, age)
}
func (a *Authentication) cookie(w http.ResponseWriter, r *http.Request, value string, age int) {
	secure := r.TLS != nil || a.Config.TrustForwardedHost && r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}
func (a *Authentication) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RevokeLogin(r.Context(), requestToken(r)); err != nil {
		authFailure(w, err)
		return
	}
	a.cookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func (a *Authentication) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RevokeAllLogins(r.Context(), requestToken(r)); err != nil {
		authFailure(w, err)
		return
	}
	a.cookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func (a *Authentication) session(w http.ResponseWriter, r *http.Request) {
	session, err := a.Store.Login(r.Context(), requestToken(r))
	if err != nil {
		authFailure(w, err)
		return
	}
	writeJSON(w, 200, session.Account)
}
func (a *Authentication) password(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, 4096, &body) {
		return
	}
	session, token, err := a.Store.ChangePassword(r.Context(), requestToken(r), body.Current, body.New, r.URL.Path == "/api/auth/password/setup")
	if err != nil {
		authFailure(w, err)
		return
	}
	a.sessionCookie(w, r, token, false)
	writeJSON(w, 200, session.Account)
}
func (a *Authentication) listUsers(w http.ResponseWriter, r *http.Request) {
	actor, err := a.Actor(r)
	if err != nil {
		authFailure(w, err)
		return
	}
	users, err := a.Store.AccountDirectory(r.Context(), actor)
	if err != nil {
		authFailure(w, err)
		return
	}
	// Share recipients use immutable IDs; labels are returned separately.
	type entry struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}
	result := make([]entry, 0, len(users))
	for _, u := range users {
		result = append(result, entry{u.ID, u.Username, u.DisplayName})
	}
	writeJSON(w, 200, map[string]any{"users": result})
}
func (a *Authentication) GC() { a.attempts.GC(); a.requests.GC() }

func (a *Authentication) IdentityInvalidations() <-chan struct{} {
	return a.Store.IdentityInvalidations()
}
