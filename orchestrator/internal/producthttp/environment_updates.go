package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"strconv"
)

func (a *Authentication) environmentUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/environments/{id}/updates/{release}", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.EnvironmentReleaseDetail(r.Context(), requestToken(r), r.PathValue("id"), r.PathValue("release"), r.URL.Query().Get("cursor"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/admin/environments/{id}/updates/preview", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := a.Store.PreviewEnvironmentProjects(r.Context(), requestToken(r), r.PathValue("id"), r.URL.Query().Get("release_id"), r.URL.Query().Get("cursor"), r.URL.Query().Get("state"), limit)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/admin/environments/{id}/updates/history", func(w http.ResponseWriter, r *http.Request) {
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		out, err := a.Store.EnvironmentReleaseHistory(r.Context(), requestToken(r), r.PathValue("id"), before)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/admin/projects/{id}/environment", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.AdminProjectEnvironment(r.Context(), requestToken(r), r.PathValue("id"), r.URL.Query().Get("cursor"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/admin/environments/{id}/updates", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := a.Store.EnvironmentUpdates(r.Context(), requestToken(r), r.PathValue("id"), r.URL.Query().Get("cursor"), r.URL.Query().Get("state"), limit)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/admin/environments/{id}/updates", func(w http.ResponseWriter, r *http.Request) {
		var input product.EnvironmentUpdateEdit
		if !decode(w, r, 32<<10, &input) {
			return
		}
		out, err := a.Store.ChangeEnvironmentUpdate(r.Context(), requestToken(r), r.PathValue("id"), input)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/me/project-configurations/{id}/environment", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Store.UserEnvironmentUpdate(r.Context(), requestToken(r), r.PathValue("id"))
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
}
