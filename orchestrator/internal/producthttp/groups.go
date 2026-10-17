package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) groupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/groups/{group}/evolution/manual", a.evolutionManual)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/audiences", a.evolutionAudienceGroups)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/grants/{account}", a.evolutionAccountGrants)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/source-resources", a.evolutionSourceResources)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/outputs", a.evolutionOutputs)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/outputs/{library}", a.evolutionOutputs)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/catalog/{kind}", a.evolutionCatalog)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/catalog/{kind}/{definition}/{revision}", a.evolutionCatalog)
	mux.HandleFunc("GET /api/admin/groups/{group}/optimization/permissions", a.evolutionPermissions)
	mux.HandleFunc("POST /api/admin/groups/{group}/evolution/backfills/preview", a.backfillPreview)
	mux.HandleFunc("GET /api/admin/groups/{group}/evolution/backfills/{backfill}", a.backfillRead)
	mux.HandleFunc("GET /api/admin/groups/{group}/evolution/backfills/{backfill}/status", a.backfillStatus)
	mux.HandleFunc("GET /api/admin/groups/{group}/evolution/backfills/{backfill}/inputs/{binding}/{port}", a.backfillContent)
	mux.HandleFunc("POST /api/admin/groups/{group}/evolution/backfills/{backfill}/{action}", a.backfillCommand)
	mux.HandleFunc("GET /api/admin/groups/{group}/corpus/collectors", a.corpusCollectors)
	mux.HandleFunc("GET /api/admin/groups/{group}/evolution/triggers", a.evolutionTriggers)
	mux.HandleFunc("POST /api/admin/groups/{group}/corpus/capture", a.corpusCapture)
	mux.HandleFunc("POST /api/admin/groups/{group}/corpus/snapshots", a.corpusSeal)
	mux.HandleFunc("GET /api/admin/groups/{group}/corpus/snapshots/{snapshot}/content", a.corpusContent)
	mux.HandleFunc("GET /api/admin/groups/{group}/corpus/bindings/{binding}/sources/{source}/frontier", a.corpusFrontier)
	mux.HandleFunc("POST /api/admin/groups/{group}/evaluation-dataset-grants", a.evaluationDatasetGrant)
	mux.HandleFunc("POST /api/admin/groups/{group}/generation-input-grants", a.generationInputGrant)
	mux.HandleFunc("GET /api/me/capabilities", a.capabilities)
	mux.HandleFunc("GET /api/admin/users/{id}/effective-access", a.effectiveAccess)
	mux.HandleFunc("POST /api/admin/groups", a.createGroup)
	mux.HandleFunc("GET /api/admin/groups/{id}", a.group)
	mux.HandleFunc("PATCH /api/admin/groups/{id}", a.editGroup)
	mux.HandleFunc("DELETE /api/admin/groups/{id}", a.deleteGroup)
	mux.HandleFunc("GET /api/admin/groups/{id}/deletion-preview", a.groupDeletionPreview)
	mux.HandleFunc("PATCH /api/admin/groups/{id}/members", a.groupMembers)
	mux.HandleFunc("GET /api/admin/groups/{id}/members", a.listGroupMembers)
	mux.HandleFunc("PUT /api/admin/groups/{id}/policies", a.groupBindings)
	mux.HandleFunc("GET /api/admin/policies", a.policies)
	mux.HandleFunc("POST /api/admin/policies", a.createPolicy)
	mux.HandleFunc("GET /api/admin/policies/{id}", a.policy)
	mux.HandleFunc("PATCH /api/admin/policies/{id}", a.editPolicy)
	mux.HandleFunc("DELETE /api/admin/policies/{id}", a.deletePolicy)
}
func (a *Authentication) capabilities(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.Capabilities(r.Context(), requestToken(r))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) effectiveAccess(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.AdminEffectiveAccess(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) createGroup(w http.ResponseWriter, r *http.Request) { a.saveGroup(w, r, "") }
func (a *Authentication) editGroup(w http.ResponseWriter, r *http.Request) {
	a.saveGroup(w, r, r.PathValue("id"))
}
func (a *Authentication) saveGroup(w http.ResponseWriter, r *http.Request, id string) {
	var input product.GroupChange
	if !decode(w, r, 8<<10, &input) {
		return
	}
	result, err := a.Store.SaveGroup(r.Context(), requestToken(r), id, input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) group(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.Group(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) groupBindings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID        string    `json:"request_id"`
		ExpectedRevision int64     `json:"expected_revision"`
		PolicyIDs        *[]string `json:"policy_ids"`
	}
	if !decode(w, r, 32<<10, &body) {
		return
	}
	if body.PolicyIDs == nil {
		adminFailure(w, product.ErrInvalid)
		return
	}
	result, err := a.Store.BindGroupPolicies(r.Context(), requestToken(r), r.PathValue("id"), product.GroupBindings{RequestID: body.RequestID, ExpectedRevision: body.ExpectedRevision, PolicyIDs: *body.PolicyIDs})
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) listGroupMembers(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	result, err := a.Store.GroupMembers(r.Context(), requestToken(r), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) groupMembers(w http.ResponseWriter, r *http.Request) {
	var input product.GroupMembers
	if !decode(w, r, 32<<10, &input) {
		return
	}
	result, err := a.Store.UpdateGroupMembers(r.Context(), requestToken(r), r.PathValue("id"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) groupDeletionPreview(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	result, err := a.Store.PreviewGroupDeletion(r.Context(), requestToken(r), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) deleteGroup(w http.ResponseWriter, r *http.Request) {
	var input product.GroupDelete
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.DeleteGroup(r.Context(), requestToken(r), r.PathValue("id"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) policies(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	result, err := a.Store.Policies(r.Context(), requestToken(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) createPolicy(w http.ResponseWriter, r *http.Request) { a.savePolicy(w, r, "") }
func (a *Authentication) editPolicy(w http.ResponseWriter, r *http.Request) {
	a.savePolicy(w, r, r.PathValue("id"))
}
func (a *Authentication) savePolicy(w http.ResponseWriter, r *http.Request, id string) {
	var input product.PolicyChange
	if !decode(w, r, 16<<10, &input) {
		return
	}
	result, err := a.Store.SavePolicy(r.Context(), requestToken(r), id, input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) policy(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.Policy(r.Context(), requestToken(r), r.PathValue("id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) deletePolicy(w http.ResponseWriter, r *http.Request) {
	var input product.AccountRevision
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.DeletePolicy(r.Context(), requestToken(r), r.PathValue("id"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
