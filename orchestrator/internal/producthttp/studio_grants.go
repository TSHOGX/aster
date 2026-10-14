package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

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
