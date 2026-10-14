package producthttp

import (
	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"strconv"
)

func (a *Authentication) setEvolutionSource(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvolutionSourcePolicyInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil || input.Source.ID != r.PathValue("source") {
		writeError(w, 400, "invalid source policy")
		return
	}
	value, err := a.Store.SetEvolutionSourcePolicy(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) evolutionSourceRevision(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("revision")
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != raw {
		writeError(w, 400, "invalid policy revision")
		return
	}
	value, err := a.Store.EvolutionSourcePolicyRevision(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("source"), revision)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionSources(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "invalid limit")
			return
		}
		limit = value
	}
	value, err := a.Store.EvolutionSources(r.Context(), requestToken(r), r.PathValue("group"), r.URL.Query().Get("after"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
