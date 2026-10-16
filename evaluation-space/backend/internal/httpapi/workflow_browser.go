package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"context"
	"errors"
	"net/http"
	"strconv"
)

// BrowserWorkflowAPI exposes the immutable draft boundary to a human session.
// Central group authority is resolved for every operation before projection access.
func BrowserWorkflowAPI(client *appidentity.Client, store *projections.Store) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	authorize := func(r *http.Request, session appidentity.Session, action string) error {
		result, err := client.EvaluationWorkflowAccess(r.Context(), session, appidentity.EvaluationWorkflowAccessRequest{GroupID: r.PathValue("group"), Action: action})
		if err != nil || !result.Allowed {
			if err != nil {
				return err
			}
			return appidentity.ErrUnauthorized
		}
		return nil
	}
	read := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "workflow.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		draft, err := store.WorkflowDraft(r.Context(), r.PathValue("workflow"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "draft_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "draft_invalid"})
			return
		}
		write(w, 200, draft)
	}
	writeDraft := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "workflow.edit"); err != nil {
			workflowAuthError(w, err)
			return
		}
		value, err := evolution.Decode[projections.WorkflowDraftInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil || value.ID != r.PathValue("workflow") || value.Workflow.ID != value.ID {
			write(w, 400, map[string]string{"error": "invalid_workflow_draft"})
			return
		}
		draft, err := store.SaveWorkflowDraft(r.Context(), value)
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "draft_conflict"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "draft_invalid"})
			return
		}
		write(w, 200, draft)
	}
	revision := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "workflow.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		n, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		if err != nil || n < 1 {
			write(w, 400, map[string]string{"error": "invalid_revision"})
			return
		}
		draft, err := store.WorkflowDraftRevision(r.Context(), r.PathValue("workflow"), n)
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "draft_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "draft_invalid"})
			return
		}
		write(w, 200, draft)
	}
	publish := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "workflow.edit"); err != nil {
			workflowAuthError(w, err)
			return
		}
		value, err := evolution.Decode[struct {
			Revision int64 `json:"revision"`
		}](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil || value.Revision < 1 {
			write(w, 400, map[string]string{"error": "invalid_publish_request"})
			return
		}
		ref, err := store.PublishWorkflowDraft(r.Context(), r.PathValue("workflow"), value.Revision)
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "draft_conflict"})
			return
		}
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "draft_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "publish_rejected"})
			return
		}
		write(w, 201, ref)
	}
	validate := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "workflow.edit"); err != nil {
			workflowAuthError(w, err)
			return
		}
		value, err := evolution.Decode[struct {
			Workflow evolution.Workflow `json:"workflow"`
		}](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_workflow_validation"})
			return
		}
		if err = value.Workflow.Validate(); err != nil {
			result := map[string]any{"error": "workflow_validation_failed"}
			var field evolution.FieldError
			if errors.As(err, &field) {
				result["fields"] = []evolution.FieldError{field}
			}
			write(w, 422, result)
			return
		}
		canonical, err := value.Workflow.Canonical()
		if err != nil {
			write(w, 422, map[string]string{"error": "workflow_validation_failed"})
			return
		}
		write(w, 200, map[string]any{"valid": true, "digest": evolution.Digest(canonical), "paid_execution": false})
	}
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/workflows/{workflow}/draft", read)
	mux.HandleFunc("PUT /evaluation-space/api/v1/groups/{group}/workflows/{workflow}/draft", writeDraft)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/workflows/{workflow}/draft/revisions/{revision}", revision)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/workflows/{workflow}/draft/publish", publish)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/workflows/{workflow}/draft/validate", validate)
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}
func contextWithBrowserSession(r *http.Request, session appidentity.Session) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), browserSessionKey{}, session))
}
func workflowAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, appidentity.ErrUnauthorized) {
		write(w, 403, map[string]string{"error": "workflow_access_denied"})
	} else {
		write(w, 503, map[string]string{"error": "workflow_authority_unavailable"})
	}
}
