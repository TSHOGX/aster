package producthttp

import (
	"errors"
	"net/http"
	"strconv"

	"harness-platform/orchestrator/internal/product"
)

func adminFailure(w http.ResponseWriter, err error) {
	if quotaFailure(w, err) {
		return
	}
	code := "request_failed"
	var admission *product.AdmissionError
	if errors.As(err, &admission) {
		code = admission.Code
	}
	status, message := 500, "administration request failed"
	switch {
	case errors.Is(err, product.ErrUnauthorized):
		status, code, message = 401, "login_required", "login required"
	case errors.Is(err, product.ErrForbidden):
		status, code, message = 403, "action_denied", "platform administrator role required"
	case errors.Is(err, product.ErrArtifactUnavailable):
		status, code, message = 503, "artifact_unavailable", "registered runtime artifact unavailable"
	case errors.Is(err, product.ErrNotFound):
		status, code, message = 404, "not_found", "resource not found"
	case errors.Is(err, product.ErrInvalid):
		status, message = 422, "invalid administration settings"
		if code == "request_failed" {
			code = "settings_invalid"
		}
	case errors.Is(err, product.ErrConflict):
		status, message = 409, "the request conflicts with current state"
		if code == "request_failed" {
			code = "revision_conflict"
		}
	case errors.Is(err, product.ErrDraining):
		status, code, message = 409, "release_fenced", "configuration changes are closed during release activation"
	}
	body := map[string]string{"code": code, "error": message}
	var field *product.SettingError
	if errors.As(err, &field) {
		body["field"] = field.Field
		body["error"] = field.Reason
	}
	writeJSON(w, status, body)
}
func adminPageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 {
		adminFailure(w, product.ErrInvalid)
		return 0, false
	}
	return n, true
}
func (a *Authentication) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/projects", a.adminProjects)
	mux.HandleFunc("GET /api/admin/projects/{id}", a.adminProject)
	a.environmentRoutes(mux)
	mux.HandleFunc("GET /api/admin/users/{id}/network", a.adminUserNetwork)
	mux.HandleFunc("PATCH /api/admin/users/{id}/network", a.editUserNetwork)
	mux.HandleFunc("GET /api/admin/network/default", a.adminNetworkDefaults)
	mux.HandleFunc("PATCH /api/admin/network/default", a.editNetworkDefaults)
	mux.HandleFunc("POST /api/admin/users/{id}/conversations/{session}/inspect", a.adminConversation)
	mux.HandleFunc("GET /api/admin/users", a.adminUsers)
	mux.HandleFunc("POST /api/admin/users", a.adminCreateUser)
	mux.HandleFunc("GET /api/admin/users/{id}", a.adminUser)
	mux.HandleFunc("PATCH /api/admin/users/{id}", a.adminEditUser)
	mux.HandleFunc("POST /api/admin/users/{id}/password-reset", a.adminResetUser)
	mux.HandleFunc("POST /api/admin/users/{id}/suspend", a.adminSuspendUser)
	mux.HandleFunc("POST /api/admin/users/{id}/enable", a.adminEnableUser)
	mux.HandleFunc("GET /api/admin/groups", a.adminGroups)
	mux.HandleFunc("GET /api/admin/audit-events", a.adminAudit)
	mux.HandleFunc("GET /api/admin/operations/{id}/targets", a.adminOperationTargets)
	mux.HandleFunc("GET /api/admin/operations/{id}", a.adminOperation)
}
func (a *Authentication) adminUsers(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	result, err := a.Store.AdminUsers(r.Context(), requestToken(r), product.AdminUserQuery{Search: q.Get("search"), Status: q.Get("status"), Role: q.Get("role"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) adminUser(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.AdminUser(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var input product.AccountCreate
	if !decode(w, r, 32<<10, &input) {
		return
	}
	result, err := a.Store.CreateAccount(r.Context(), requestToken(r), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	status := 201
	if result.Replayed {
		status = 200
	}
	writeJSON(w, status, result)
}
func (a *Authentication) adminEditUser(w http.ResponseWriter, r *http.Request) {
	var input product.AccountEdit
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.UpdateAccount(r.Context(), requestToken(r), r.PathValue("id"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) adminResetUser(w http.ResponseWriter, r *http.Request) {
	var input product.AccountRevision
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.ResetAccountPassword(r.Context(), requestToken(r), r.PathValue("id"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) adminSuspendUser(w http.ResponseWriter, r *http.Request) {
	a.adminStatus(w, r, false)
}
func (a *Authentication) adminEnableUser(w http.ResponseWriter, r *http.Request) {
	a.adminStatus(w, r, true)
}
func (a *Authentication) adminStatus(w http.ResponseWriter, r *http.Request, enable bool) {
	var input product.AccountRevision
	if !decode(w, r, 4096, &input) {
		return
	}
	var result product.AccountMutation
	var err error
	if enable {
		result, err = a.Store.EnableAccount(r.Context(), requestToken(r), r.PathValue("id"), input)
	} else {
		result, err = a.Store.SuspendAccount(r.Context(), requestToken(r), r.PathValue("id"), input)
	}
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 202, result)
}
func (a *Authentication) adminGroups(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	result, err := a.Store.AdminGroups(r.Context(), requestToken(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) adminAudit(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	result, err := a.Store.AdminAudit(r.Context(), requestToken(r), product.AuditQuery{Actor: q.Get("actor"), Target: q.Get("target"), Action: q.Get("action"), After: q.Get("after"), Before: q.Get("before"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) adminOperation(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.AdminOperation(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *Authentication) adminOperationTargets(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	value, err := a.Store.AdminOperationTargets(r.Context(), requestToken(r), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
