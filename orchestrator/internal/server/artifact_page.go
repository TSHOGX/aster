package server

import (
	"harness-platform/orchestrator/internal/store"
	"net/http"
	"strings"
)

func (s *Server) artifactPage(w http.ResponseWriter, r *http.Request, id string) {
	if _, _, _, ok := s.authorizeRead(w, r, id); !ok {
		return
	}
	if r.URL.Query().Get("cursor") == "" && !s.refreshRetainedArtifacts(w, r, id) {
		return
	}
	s.serveArtifactPage(w, r, store.ArtifactPageParams{SessionID: id})
}
func (s *Server) serveArtifactPage(w http.ResponseWriter, r *http.Request, p store.ArtifactPageParams) {
	p.Search = strings.HasSuffix(r.URL.Path, "/search")
	p.Directory = r.URL.Query().Get("directory")
	p.Query = r.URL.Query().Get("q")
	p.Cursor = r.URL.Query().Get("cursor")
	var err error
	p.Limit, err = pageNumber(r, "limit", 200, 500)
	if err != nil || p.Limit == 0 {
		writeError(w, 400, "invalid limit")
		return
	}
	page, err := s.store.ArtifactPage(r.Context(), p)
	if err != nil {
		writeError(w, 400, "cannot read artifact page")
		return
	}
	writeJSON(w, 200, page)
}
