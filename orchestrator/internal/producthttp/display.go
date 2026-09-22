package producthttp

import (
	"net/http"
	"strconv"

	"harness-platform/orchestrator/internal/product"
)

func (s *Server) conversationHead(w http.ResponseWriter, r *http.Request) {
	head, err := s.Store.ConversationHead(r.Context(), s.scope(r), r.PathValue("session"))
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusOK
	if head.State == "building" {
		status = http.StatusAccepted
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, head)
}
func (s *Server) conversationPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := s.Store.ConversationPage(r.Context(), s.scope(r), r.PathValue("session"), product.ViewSelector{Before: q.Get("before"), After: q.Get("after"), AroundTurn: q.Get("around_turn"), AroundMessage: q.Get("around_message")})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) conversationChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	base, err := strconv.ParseInt(q.Get("after_revision"), 10, 64)
	if err != nil || base < 0 {
		fail(w, product.ErrInvalid)
		return
	}
	changes, err := s.Store.ConversationChanges(r.Context(), s.scope(r), r.PathValue("session"), base, q.Get("cursor"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, changes)
}
func (s *Server) conversationDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	detail, err := s.Store.ConversationDetail(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("item"), q.Get("version"), q.Get("cursor"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
func (s *Server) rebuildConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RebuildConversation(r.Context(), s.scope(r), r.PathValue("session")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "building"})
}
