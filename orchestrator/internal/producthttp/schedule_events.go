package producthttp

import (
	"net/http"

	"harness-platform/orchestrator/internal/product"
)

func (s *Server) scheduleEvents(w http.ResponseWriter, r *http.Request) {
	scope := product.Scope{Actor: actorFrom(r.Context())}
	if _, _, err := s.Store.ScheduleEventBounds(r.Context(), scope.Actor); err != nil {
		fail(w, err)
		return
	}
	s.streamResources(w, r, scope)
}
