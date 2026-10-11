package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) environmentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/runtime/default", a.adminEnvironment)
	mux.HandleFunc("POST /api/admin/runtime/default/preview", a.previewEnvironment)
	mux.HandleFunc("PATCH /api/admin/runtime/default", a.editEnvironment)
}
func (a *Authentication) adminEnvironment(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.AdminEnvironment(r.Context(), requestToken(r))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) previewEnvironment(w http.ResponseWriter, r *http.Request) {
	var input product.EnvironmentChange
	if !decode(w, r, 256<<10, &input) {
		return
	}
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	value, err := a.Store.PreviewEnvironment(r.Context(), requestToken(r), input, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) editEnvironment(w http.ResponseWriter, r *http.Request) {
	var input product.EnvironmentEdit
	if !decode(w, r, 256<<10, &input) {
		return
	}
	value, err := a.Store.EditEnvironment(r.Context(), requestToken(r), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
