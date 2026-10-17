package producthttp

import (
	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) evaluationDatasetGrant(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvaluationDatasetGrantInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid dataset grant")
		return
	}
	revision, err := a.Store.SetEvaluationDatasetGrant(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"revision": revision})
}
func (a *Authentication) identityEvaluationAdmission(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "studio" {
		writeError(w, 403, "studio service required")
		return
	}
	input, err := evolution.Decode[evolution.EvaluationAssignment](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil || input.Requester.Issuer != a.Config.Identity.Issuer {
		writeError(w, 400, "invalid evaluation assignment")
		return
	}
	grant, err := a.Store.AuthorizeEvaluationAssignment(r.Context(), input)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, grant)
}
func (a *Authentication) identityEvaluationAuthority(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "studio" && client.ID != "evolution-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	input, err := evolution.Decode[evolution.EvaluationGrantScope](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid evaluation scope")
		return
	}
	if client.ID != "evolution-space" && (client.ID != "studio" || (input.Action != "read" && input.Action != "input")) {
		writeError(w, 403, "evaluation service required")
		return
	}
	assignment, err := a.Store.ResolveEvaluationGrant(r.Context(), input)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, assignment)
}
func (a *Authentication) identityEvaluationSettlement(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evolution-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	input, err := evolution.Decode[product.EvaluationSettlement](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid evaluation settlement")
		return
	}
	if err = a.Store.SettleEvaluationGrant(r.Context(), input); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *Authentication) identityEvaluationProfile(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evolution-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	input, err := evolution.Decode[evolution.EvaluationProfileScope](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || input.Requester.Issuer != a.Config.Identity.Issuer {
		writeError(w, 400, "invalid profile scope")
		return
	}
	if err = a.Store.AuthorizeEvaluationProfile(r.Context(), input); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
