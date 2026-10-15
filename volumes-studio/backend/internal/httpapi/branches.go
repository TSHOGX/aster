package httpapi

import (
	"mime"
	"net/http"
	"strings"
	"time"

	"volume-studio/backend/internal/authoring"
)

func (a *AuthoringAPI) registerBranches() {
	root := apiPrefix + "libraries/{library}/branches"
	a.mux.HandleFunc("GET "+root, func(w http.ResponseWriter, r *http.Request) {
		page, err := a.Store.Branches(r.Context(), actor(r), r.PathValue("library"), r.URL.Query().Get("after"))
		respond(w, 200, page, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/draft/entry", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.BranchEntry(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), r.URL.Query().Get("path"), revision(r))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/draft/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := a.Store.SearchBranch(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), q.Get("q"), q.Get("cursor"), revision(r))
		respond(w, 200, map[string]any{"items": page.Entries, "next": page.Next, "total": page.DescendantCount, "revision": page.Revision}, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/draft/validation", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.BranchValidation(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), revision(r))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/head-file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		value, err := a.Store.BranchHeadFile(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), q.Get("head"), q.Get("path"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root, func(w http.ResponseWriter, r *http.Request) {
		var input authoring.CreateBranchInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.CreateBranch(r.Context(), actor(r), r.PathValue("library"), input)
		respond(w, 201, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Store.Branch(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/draft/tree", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		value, err := a.Store.BranchTree(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), q.Get("parent"), q.Get("after"), revision(r))
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/draft/diff", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := a.Store.BranchDiff(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), q.Get("head"), q.Get("after"), revision(r))
		respond(w, 200, page, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/head-diff", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := a.Store.BranchHeadDiff(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), q.Get("head"), q.Get("after"))
		respond(w, 200, page, err)
	})
	a.mux.HandleFunc("GET "+root+"/{branch}/draft/content", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Range"), ",") {
			writeError(w, 416, "single_range_required")
			return
		}
		f, entry, err := a.Store.BranchContent(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), r.URL.Query().Get("path"), revision(r))
		if err != nil {
			authoringError(w, err)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Path}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, entry.Path, time.Time{}, f)
	})
	a.mux.HandleFunc("POST "+root+"/{branch}/draft/text", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.SaveBranchTextInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.SaveBranchText(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/{branch}/draft/structure", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.BranchStructureInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.MutateBranchStructure(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), input)
		respond(w, 200, value, err)
	})
	a.mux.HandleFunc("POST "+root+"/{branch}/commits", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.CheckpointInput
		if !decodeBody(w, r, &input) {
			return
		}
		value, err := a.Store.Checkpoint(r.Context(), actor(r), r.PathValue("library"), r.PathValue("branch"), input)
		respond(w, 201, value, err)
	})
}
