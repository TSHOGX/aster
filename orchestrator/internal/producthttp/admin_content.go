package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) adminConversation(w http.ResponseWriter, r *http.Request) {
	var input product.AdminConversationRequest
	if !decode(w, r, 8<<10, &input) {
		return
	}
	page, err := a.Store.AdminConversation(r.Context(), requestToken(r), r.PathValue("id"), r.PathValue("session"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, page)
}
