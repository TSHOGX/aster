package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"strings"
)

func (a *Authentication) adminProjects(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	value, err := a.Store.AdminProjects(r.Context(), requestToken(r), product.AdminProjectQuery{OwnerID: query.Get("owner_id"), State: query.Get("state"), Search: query.Get("search"), Cursor: query.Get("cursor"), Limit: limit})
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) adminProject(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.AdminProject(r.Context(), requestToken(r), r.PathValue("id"), a.ProjectInspector)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) adminControlProject(w http.ResponseWriter, r *http.Request) {
	var input product.AdminProjectControl
	if !decode(w, r, 16<<10, &input) {
		return
	}
	_, action, _ := strings.Cut(r.URL.Path, "/api/admin/projects/"+r.PathValue("id")+"/")
	if action == "apply-network" {
		action = "apply_network"
	}
	value, err := a.Store.AdminControlProject(r.Context(), requestToken(r), r.PathValue("id"), action, input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 202, value)
}
