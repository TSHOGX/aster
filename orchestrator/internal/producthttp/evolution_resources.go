package producthttp

import (
	"net/http"
	"strconv"
)

func (a *Authentication) evolutionSourceResources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "invalid source resource limit")
			return
		}
	}
	value, err := a.Store.EvolutionSourceResources(r.Context(), requestToken(r), r.PathValue("group"), r.URL.Query().Get("kind"), r.URL.Query().Get("search"), r.URL.Query().Get("after"), limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
