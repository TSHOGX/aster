package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) adminNetworkDefaults(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.AdminNetworkDefaults(r.Context(), requestToken(r))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) editNetworkDefaults(w http.ResponseWriter, r *http.Request) {
	var input product.NetworkDefaultEdit
	if !decode(w, r, 64<<10, &input) {
		return
	}
	value, err := a.Store.EditNetworkDefaults(r.Context(), requestToken(r), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
