package httpapi

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *AuthoringAPI) registerHistory() {
	root := apiPrefix + "libraries/{library}"
	a.mux.HandleFunc("GET "+root+"/commits/{commit}/content", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Range"), ",") {
			writeError(w, 416, "single_range_required")
			return
		}
		reader, entry, err := a.Store.CommitContent(r.Context(), actor(r), r.PathValue("library"), r.PathValue("commit"), r.URL.Query().Get("path"))
		if err != nil {
			authoringError(w, err)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Path}))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, entry.Path, time.Time{}, reader)
	})
	a.mux.HandleFunc("GET "+root+"/history", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 50
		if raw := q.Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || strconv.Itoa(value) != raw {
				writeError(w, 422, "invalid_limit")
				return
			}
			limit = value
		}
		page, err := a.Store.History(r.Context(), actor(r), r.PathValue("library"), q.Get("head"), q.Get("cursor"), limit)
		respond(w, 200, page, err)
	})
	a.mux.HandleFunc("GET "+root+"/compare", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := a.Store.CompareSnapshots(r.Context(), actor(r), r.PathValue("library"), q.Get("base"), q.Get("target"), q.Get("cursor"))
		respond(w, 200, page, err)
	})
	a.mux.HandleFunc("GET "+root+"/commits/{commit}/file", func(w http.ResponseWriter, r *http.Request) {
		file, err := a.Store.CommitFile(r.Context(), actor(r), r.PathValue("library"), r.PathValue("commit"), r.URL.Query().Get("path"))
		respond(w, 200, file, err)
	})
}
