package producthttp

import (
	"net/http"
)

func (a *Authentication) groupEvolutionCatalog(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.GroupCatalog(r.Context(), requestToken(r), r.PathValue("group"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"catalog": value})
}
