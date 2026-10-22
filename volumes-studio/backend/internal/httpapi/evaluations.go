package httpapi

import (
	"net/http"
	"volume-studio/backend/internal/authoring"
)

func (a *AuthoringAPI) registerEvaluations() {
	root := apiPrefix + "libraries/{library}"
	a.mux.HandleFunc("POST "+root+"/evaluations/{job}/cancel", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.CancelEvaluation(r.Context(), actor(r), r.PathValue("library"), r.PathValue("job"))
		respond(w, 202, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/integrations/{digest}/evaluations", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.EvaluationRequestInput
		if !decodeBody(w, r, &input) {
			return
		}
		result, err := a.Store.RequestEvaluation(r.Context(), actor(r), r.PathValue("library"), r.PathValue("digest"), input)
		respond(w, 202, result, err)
	})
	a.mux.HandleFunc("POST "+root+"/evaluations/{job}/retry", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Store.ReconcileEvaluation(r.Context(), actor(r), r.PathValue("library"), r.PathValue("job"))
		respond(w, 202, result, err)
	})
	a.mux.HandleFunc("POST "+root+"/evaluations/{job}/refresh", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Store.RefreshEvaluation(r.Context(), actor(r), r.PathValue("library"), r.PathValue("job"))
		respond(w, 200, result, err)
	})
}
