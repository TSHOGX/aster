package httpapi

import "net/http"

func (a *AuthoringAPI) libraryAction(action string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.AuthorizeLibrary(r.Context(), actor(r), r.PathValue("library"), action); err != nil {
			authoringError(w, err)
			return
		}
		next(w, r)
	}
}
