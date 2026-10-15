package httpapi

import (
	"net/http"
	"volume-studio/backend/internal/authoring"
)

func (a *AuthoringAPI) registerReviews() {
	root := apiPrefix + "libraries/{library}"
	a.mux.HandleFunc("GET "+root+"/conflicts/{workspace}/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		value, err := a.Store.ConflictFile(r.Context(), actor(r), r.PathValue("library"), r.PathValue("workspace"), q.Get("side"), q.Get("path"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/conflicts/{workspace}", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.ConflictWorkspace(r.Context(), actor(r), r.PathValue("library"), r.PathValue("workspace"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/conflicts/{workspace}/resolutions", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.ConflictResolutionInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.SaveConflictResolution(r.Context(), actor(r), r.PathValue("library"), r.PathValue("workspace"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/conflicts/{workspace}/complete", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.CompleteConflictInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.CompleteConflict(r.Context(), actor(r), r.PathValue("library"), r.PathValue("workspace"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/integrations/{digest}/changes", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.IntegrationChanges(r.Context(), actor(r), r.PathValue("library"), r.PathValue("digest"), r.URL.Query().Get("after"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/integrations/{digest}/file", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.IntegrationFile(r.Context(), actor(r), r.PathValue("library"), r.PathValue("digest"), r.URL.Query().Get("side"), r.URL.Query().Get("path"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/integrations/{digest}", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.Integration(r.Context(), actor(r), r.PathValue("library"), r.PathValue("digest"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/main", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.MainCommit(r.Context(), actor(r), r.PathValue("library"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/merge-policy", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.MergePolicy(r.Context(), actor(r), r.PathValue("library"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/merge-policy", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.MergePolicyInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.SetMergePolicy(r.Context(), actor(r), r.PathValue("library"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/integrations/prepare", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.PrepareInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.PrepareMerge(r.Context(), actor(r), r.PathValue("library"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/integrations/compose", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.ComposeInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.ComposeMerge(r.Context(), actor(r), r.PathValue("library"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/integrations/reviews", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.ReviewInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.Review(r.Context(), actor(r), r.PathValue("library"), input)
		respond(w, 201, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/integrations/merge", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.MergeInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.Merge(r.Context(), actor(r), r.PathValue("library"), input)
		respond(w, 200, value, err)
	})
}
