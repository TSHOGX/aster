package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) publicViewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/shared/{token}/view/head", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Store.BundleHead(r.Context(), r.PathValue("token"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/shared/{token}/view/pages", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.Store.BundlePage(r.Context(), r.PathValue("token"), product.ViewSelector{Before: q.Get("before"), After: q.Get("after"), AroundTurn: q.Get("around_turn"), AroundMessage: q.Get("around_message"), AroundItem: q.Get("around_item")})
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/shared/{token}/view/turns", func(w http.ResponseWriter, r *http.Request) {
		ids := []string{}
		if raw := r.URL.Query().Get("ids"); raw != "" {
			ids = strings.Split(raw, ",")
		}
		out, err := s.Store.BundleTurns(r.Context(), r.PathValue("token"), ids)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/shared/{token}/view/items/{item}/detail", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Store.BundleText(r.Context(), r.PathValue("token"), r.PathValue("item"), false, r.URL.Query().Get("cursor"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/shared/{token}/view/messages/{message}/content", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Store.BundleText(r.Context(), r.PathValue("token"), r.PathValue("message"), true, r.URL.Query().Get("cursor"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/shared/{token}/view/changes", func(w http.ResponseWriter, r *http.Request) {
		head, err := s.Store.BundleHead(r.Context(), r.PathValue("token"))
		if err != nil {
			fail(w, err)
			return
		}
		if r.URL.Query().Get("after_revision") != "1" {
			fail(w, product.ErrResync)
			return
		}
		writeJSON(w, 200, map[string]any{"projection_generation": head.Generation, "base_revision": "1", "target_revision": "1", "processed_through": "1", "changes": []any{}, "has_more": false})
	})
	mux.HandleFunc("GET /api/shared/{token}/turn-index", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.Store.BundleIndex(r.Context(), r.PathValue("token"), int64Param(q, "before"), int64Param(q, "after"), intParam(q, "limit", 100))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/shared/{token}/events", func(w http.ResponseWriter, r *http.Request) {
		s.sharedEvents(w, r, func() (string, error) {
			share, err := s.Store.PublicShare(r.Context(), r.PathValue("token"))
			return strconv.FormatInt(share.AccessRevision, 10), err
		})
	})
	mux.HandleFunc("GET /api/shared/{token}/files/content", s.publicFile)
	mux.HandleFunc("GET /api/shared/{token}/files/download", s.publicFile)
}

func (s *Server) updatePublicPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision      int64 `json:"revision"`
		AllowDownload bool  `json:"allow_download"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if err := s.Store.SetPublicDownload(r.Context(), s.scope(r), r.PathValue("share"), body.Revision, body.AllowDownload); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
