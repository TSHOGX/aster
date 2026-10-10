package producthttp

import (
	"errors"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (s *Server) resumeProject(w http.ResponseWriter, r *http.Request) {
	if err := s.Lifecycle.Resume(r.Context(), s.scope(r), s.Profile); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}
func (s *Server) agentControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		product.ExecutionControl
		Revision int64 `json:"revision"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	var err error
	switch body.Action {
	case "retry_recovery":
		err = s.Store.RetryRecovery(r.Context(), s.scope(r), r.PathValue("session"), body.Revision)
	case "continue":
		err = s.Store.ContinueSession(r.Context(), s.scope(r), r.PathValue("session"), body.Revision)
	case "stop", "steer":
		receipt, e := s.Store.ControlRun(r.Context(), s.scope(r), r.PathValue("session"), body.ExecutionControl)
		if e != nil {
			controlFailure(w, e)
			return
		}
		writeJSON(w, http.StatusAccepted, receipt)
		return
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
		Action                 string `json:"action"`
		RequestID              string `json:"request_id"`
		ExpectedPromptRevision int64  `json:"expected_prompt_revision"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	receipt, err := s.Store.MutatePrompt(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("turn"), body.RequestID, body.Action, body.ExpectedPromptRevision)
	if err != nil {
		controlFailure(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func (s *Server) turnIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := s.Store.TurnIndex(r.Context(), s.scope(r), r.PathValue("session"), int64Param(q, "before"), int64Param(q, "after"), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// A failed admission never returns 202. Accepted requests have durable receipts.
func controlFailure(w http.ResponseWriter, err error) {
	code, message := status(err)
	if code == http.StatusAccepted {
		code = http.StatusConflict
	}
	admissionCode := "control_not_admitted"
	var admission *product.AdmissionError
	if errors.As(err, &admission) {
		admissionCode = admission.Code
	}
	writeJSON(w, code, map[string]string{"state": "not_admitted", "code": admissionCode, "error": message})
}

func (s *Server) controlResult(w http.ResponseWriter, r *http.Request) {
	result, err := s.Store.ControlResult(r.Context(), s.scope(r), r.PathValue("session"), r.PathValue("request"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
