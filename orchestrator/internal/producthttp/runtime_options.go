package producthttp

import "net/http"

func (a *Authentication) runtimeOptions(w http.ResponseWriter, r *http.Request) {
	limit, ok := adminPageLimit(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "sandbox"
	}
	value, err := a.Store.AdminRuntimeOptions(r.Context(), requestToken(r), kind, r.URL.Query().Get("cursor"), limit, a.RuntimeInspector)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
