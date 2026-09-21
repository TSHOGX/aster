package producthttp

import (
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (s *Server) resumeProject(w http.ResponseWriter, r *http.Request) {
	if err := s.Lifecycle.EnsureActive(r.Context(), s.scope(r), s.Profile); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}
func (s *Server) sessionSnapshot(w http.ResponseWriter, r *http.Request) {
	before := int64Param(r.URL.Query(), "before")
	if message := r.URL.Query().Get("message"); message != "" {
		sequence, err := s.Store.MessageSequence(r.Context(), s.scope(r), r.PathValue("session"), message)
		if err != nil {
			fail(w, err)
			return
		}
		before = sequence + 51
	}
	snapshot, err := s.Store.SessionSnapshot(r.Context(), s.scope(r), r.PathValue("session"), before)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, snapshot)
}
func (s *Server) agentRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	records, err := s.Store.AgentRecords(r.Context(), s.scope(r), r.PathValue("session"), int64Param(q, "after"), intParam(q, "limit", 200))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"records": records})
}
func (s *Server) agentControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action    string `json:"action"`
		RequestID string `json:"request_id"`
		Source    string `json:"source"`
		Revision  int64  `json:"revision"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	var err error
	switch body.Action {
	case "interrupt":
		err = s.Store.Interrupt(r.Context(), s.scope(r), r.PathValue("session"), body.RequestID, body.Revision)
	case "steer":
		err = s.Store.Steer(r.Context(), s.scope(r), r.PathValue("session"), body.Source, body.RequestID, body.Revision)
	default:
		err = product.ErrInvalid
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "pending"})
}
func (s *Server) queuedControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action   string `json:"action"`
		Revision int64  `json:"revision"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	var err error
	if body.Action == "release" {
		err = s.Store.ReleaseTurn(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("turn"), body.Revision)
	} else {
		err = s.Store.ChangeQueuedTurn(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("turn"), body.Action, body.Revision)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "updated"})
}
