package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) publishEvolutionCatalog(w http.ResponseWriter, r *http.Request) {
	var c product.CatalogRevision
	if !decode(w, r, 1<<20, &c) {
		return
	}
	value, err := a.Store.PublishCatalogRevision(r.Context(), requestToken(r), c)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 201, value)
}
func (a *Authentication) bindEvolutionCatalog(w http.ResponseWriter, r *http.Request) {
	var input struct {
		GroupID   string `json:"group_id"`
		BindingID string `json:"binding_id"`
		Kind      string `json:"kind"`
		CatalogID string `json:"catalog_id"`
		Revision  int64  `json:"revision"`
	}
	if !decode(w, r, 1<<16, &input) {
		return
	}
	value, err := a.Store.BindCatalogRevision(r.Context(), requestToken(r), input.GroupID, input.BindingID, input.Kind, input.CatalogID, input.Revision)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
