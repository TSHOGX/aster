package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) resourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/groups/{id}/resource-policy", a.groupResourcePolicy)
	mux.HandleFunc("PATCH /api/admin/groups/{id}/resource-policy", a.editGroupResources)
	mux.HandleFunc("GET /api/admin/users/{id}/resource-settings", a.userResourceSettings)
	mux.HandleFunc("PATCH /api/admin/users/{id}/resource-settings", a.editUserResources)
	mux.HandleFunc("GET /api/admin/users/{id}/usage", a.adminUserUsage)
	mux.HandleFunc("POST /api/admin/changes/preview", a.resourceChangePreview)
	mux.HandleFunc("GET /api/me/usage", a.ownUsage)
}
func (a *Authentication) groupResourcePolicy(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.GroupResourcePolicy(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) userResourceSettings(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.UserResourceSettings(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

type resourceEdit struct {
	RequestID            string `json:"request_id"`
	ExpectedRevision     int64  `json:"expected_revision"`
	ExpectedDependencies string `json:"expected_dependencies"`
}

func (a *Authentication) editGroupResources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		resourceEdit
		Dimensions map[product.ResourceDimension]*product.ResourceEntitlement `json:"dimensions"`
	}
	if !decode(w, r, 64<<10, &body) {
		return
	}
	value, err := a.Store.ApplyResourceChange(r.Context(), requestToken(r), product.ResourceMutation{ResourceChange: product.ResourceChange{Kind: "group", TargetID: r.PathValue("id"), ExpectedRevision: body.ExpectedRevision, Dimensions: body.Dimensions}, RequestID: body.RequestID, ExpectedDependencies: body.ExpectedDependencies})
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) editUserResources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		resourceEdit
		Overrides product.ResourceOverrides `json:"overrides"`
	}
	if !decode(w, r, 64<<10, &body) {
		return
	}
	value, err := a.Store.ApplyResourceChange(r.Context(), requestToken(r), product.ResourceMutation{ResourceChange: product.ResourceChange{Kind: "user", TargetID: r.PathValue("id"), ExpectedRevision: body.ExpectedRevision, Overrides: body.Overrides}, RequestID: body.RequestID, ExpectedDependencies: body.ExpectedDependencies})
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) resourceChangePreview(w http.ResponseWriter, r *http.Request) {
	var body product.ResourceChange
	if !decode(w, r, 64<<10, &body) {
		return
	}
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	value, err := a.Store.PreviewResourceChange(r.Context(), requestToken(r), body, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) adminUserUsage(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.AdminUserUsage(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) ownUsage(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.OwnUsage(r.Context(), requestToken(r))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
