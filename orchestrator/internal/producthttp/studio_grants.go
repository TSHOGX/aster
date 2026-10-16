package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) inspectStudioGrants(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	result, err := a.Store.InspectStudioGrants(r.Context(), requestToken(r), query.Get("kind"), query.Get("subject_id"), query.Get("library_id"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *Authentication) editStudioGrant(w http.ResponseWriter, r *http.Request) {
	var input product.StudioGrantCommand
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.ApplyStudioGrant(r.Context(), requestToken(r), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
