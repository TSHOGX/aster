package producthttp

import "net/http"

func (a *Authentication) evolutionAccountGrants(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	value, err := a.Store.EvolutionAccountGrants(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("account"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
