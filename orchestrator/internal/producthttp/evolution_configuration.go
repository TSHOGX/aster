package producthttp

import (
	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) evolutionDraft(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("group")
	if r.Method == http.MethodGet {
		value, err := a.Store.EvolutionDraft(r.Context(), requestToken(r), group)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, value)
		return
	}
	input, err := evolution.Decode[product.EvolutionDraftInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil {
		writeError(w, 400, "invalid configuration draft")
		return
	}
	value, err := a.Store.SaveEvolutionDraft(r.Context(), requestToken(r), group, input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionGroupGrant(w http.ResponseWriter, r *http.Request) {
	var input product.EvolutionGroupGrantInput
	if !decode(w, r, 1<<16, &input) {
		return
	}
	revision, err := a.Store.SetEvolutionGroupGrant(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"revision": revision})
}
