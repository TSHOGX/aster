package httpapi

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"errors"
	"mime"
	"net/http"
	"strconv"
)

type WorkflowDraftScope struct{ WorkflowID string }
type WorkflowDraftAuthority interface {
	AuthorizeWorkflowDraft(*http.Request, WorkflowDraftScope, string) error
}
type WorkflowDraftAPI struct {
	Store     *projections.Store
	Authority WorkflowDraftAuthority
}

func (a WorkflowDraftAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evaluation-space/api/v1/workflows/{workflow}/draft", a.read)
	mux.HandleFunc("PUT /evaluation-space/api/v1/workflows/{workflow}/draft", a.write)
	return mux
}
func (a WorkflowDraftAPI) authorized(w http.ResponseWriter, r *http.Request, action string) bool {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return false
	}
	if a.Store == nil || a.Authority == nil {
		write(w, 503, map[string]string{"error": "draft_authority_unavailable"})
		return false
	}
	if err := a.Authority.AuthorizeWorkflowDraft(r, WorkflowDraftScope{r.PathValue("workflow")}, action); err != nil {
		write(w, 403, map[string]string{"error": "draft_authority_denied"})
		return false
	}
	return true
}
func (a WorkflowDraftAPI) read(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r, "read") {
		return
	}
	value, err := a.Store.WorkflowDraft(r.Context(), r.PathValue("workflow"))
	if errors.Is(err, projections.ErrNotFound) {
		write(w, 404, map[string]string{"error": "draft_not_found"})
		return
	}
	if err != nil {
		write(w, 422, map[string]string{"error": "draft_invalid"})
		return
	}
	write(w, 200, value)
}
func (a WorkflowDraftAPI) write(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r, "write") {
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return
	}
	value, err := evolution.Decode[projections.WorkflowDraftInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil || value.Workflow.ID != r.PathValue("workflow") {
		write(w, 400, map[string]string{"error": "invalid_workflow_draft"})
		return
	}
	result, err := a.Store.SaveWorkflowDraft(r.Context(), value)
	if errors.Is(err, projections.ErrConflict) {
		write(w, 409, map[string]string{"error": "draft_conflict"})
		return
	}
	if err != nil {
		write(w, 422, map[string]string{"error": "draft_invalid"})
		return
	}
	write(w, 200, result)
}

func (a WorkflowDraftAPI) revision(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r, "read") {
		return
	}
	value, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || value < 1 || strconv.FormatInt(value, 10) != r.PathValue("revision") {
		write(w, 400, map[string]string{"error": "invalid_draft_revision"})
		return
	}
	draft, err := a.Store.WorkflowDraftRevision(r.Context(), r.PathValue("workflow"), value)
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
