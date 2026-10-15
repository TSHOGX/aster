package httpapi

import (
	"aster/contracts/appidentity"
	"aster/evaluation-space/internal/projections"
	"errors"
	"net/http"
	"strconv"
)

// BrowserEvaluationAPI exposes immutable evaluation metadata and settled
// reports after the current group grant has been checked. Dataset bytes and
// evaluator credentials remain on private service routes.
func BrowserEvaluationAPI(client *appidentity.Client, store *projections.Store) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	authorize := func(r *http.Request, session appidentity.Session) error {
		result, err := client.EvaluationGroups(r.Context(), session, appidentity.EvaluationAccessRequest{GroupID: r.PathValue("group"), Action: "config.read", Limit: 1})
		if err != nil {
			return err
		}
		if len(result.Items) != 1 || result.Items[0].ID != r.PathValue("group") {
			return appidentity.ErrUnauthorized
		}
		return nil
	}
	read := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session); err != nil {
			workflowAuthError(w, err)
			return
		}
		job, err := store.Evaluation(r.Context(), r.PathValue("group"), r.PathValue("job"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "evaluation_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "evaluation_unavailable"})
			return
		}
		// Grant and dataset credentials are never rendered in the browser view.
		job.Grant = nil
		write(w, 200, job)
	}
	list := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session); err != nil {
			workflowAuthError(w, err)
			return
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if r.URL.Query().Get("limit") == "" {
			limit = 25
		}
		if err != nil || limit < 1 || limit > 50 {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
		page, err := store.Evaluations(r.Context(), r.PathValue("group"), r.URL.Query().Get("after"), limit)
		if err != nil {
			write(w, 422, map[string]string{"error": "evaluations_unavailable"})
			return
		}
		write(w, 200, page)
	}
	result := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session); err != nil {
			workflowAuthError(w, err)
			return
		}
		completion, err := store.EvaluationResult(r.Context(), r.PathValue("group"), r.PathValue("job"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "evaluation_result_pending"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "evaluation_result_unavailable"})
			return
		}
		write(w, 200, completion)
	}
	cancel := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if result, err := client.EvaluationGroups(r.Context(), session, appidentity.EvaluationAccessRequest{GroupID: r.PathValue("group"), Action: "run.start", Limit: 1}); err != nil || len(result.Items) != 1 || result.Items[0].ID != r.PathValue("group") {
			if err == nil {
				err = appidentity.ErrUnauthorized
			}
			workflowAuthError(w, err)
			return
		}
		if err := store.CancelEvaluation(r.Context(), r.PathValue("group"), r.PathValue("job")); errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "evaluation_not_found"})
			return
		} else if err != nil {
			write(w, 409, map[string]string{"error": "evaluation_cancel_rejected"})
			return
		}
		write(w, 200, map[string]string{"state": "cancel_requested"})
	}
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/evaluations", list)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/evaluations/{job}", read)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/evaluations/{job}/result", result)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/evaluations/{job}/cancel", cancel)
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}
