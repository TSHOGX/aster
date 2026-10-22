package httpapi

import (
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"errors"
	"mime"
	"net/http"
	"strconv"
)

type RunScope struct {
	GroupID string
	RunID   string
}
type RunAuthorizer interface {
	AuthorizeRun(*http.Request, RunScope, string) error
}
type RunAPI struct {
	Store     *projections.Store
	Authority RunAuthorizer
}

func (r RunAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/runs/{run}", r.read)
	mux.HandleFunc("POST /evolution-space/api/v1/groups/{group}/runs/{run}/control", r.control)
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/runs/{run}/events", r.events)
	return mux
}
func (r RunAPI) read(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req, "read") {
		return
	}
	run, err := r.Store.Run(req.Context(), req.PathValue("group"), req.PathValue("run"))
	if errors.Is(err, projections.ErrNotFound) {
		write(w, 404, map[string]string{"error": "run_not_found"})
		return
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "run_store_unavailable"})
		return
	}
	write(w, 200, run)
}
func (r RunAPI) control(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Cookie") != "" || req.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	if !r.authorize(w, req, "control") {
		return
	}
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return
	}
	value, err := evolution.Decode[projections.RunControl](http.MaxBytesReader(w, req.Body, 1<<16))
	if err != nil {
		write(w, 400, map[string]string{"error": "invalid_run_control"})
		return
	}
	if value.RunID != req.PathValue("run") {
		write(w, 409, map[string]string{"error": "run_scope_mismatch"})
		return
	}
	value.GroupID = req.PathValue("group")
	result, err := r.Store.ControlRunScoped(req.Context(), value.GroupID, value)
	if errors.Is(err, projections.ErrConflict) {
		write(w, 409, map[string]string{"error": "run_control_conflict"})
		return
	}
	if err != nil {
		write(w, 422, map[string]string{"error": "run_control_rejected"})
		return
	}
	write(w, 200, result)
}
func (r RunAPI) authorize(w http.ResponseWriter, req *http.Request, action string) bool {
	if r.Store == nil || r.Authority == nil {
		write(w, 503, map[string]string{"error": "run_authority_unavailable"})
		return false
	}
	if req.Header.Get("Cookie") != "" || req.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return false
	}
	if err := r.Authority.AuthorizeRun(req, RunScope{GroupID: req.PathValue("group"), RunID: req.PathValue("run")}, action); err != nil {
		write(w, 403, map[string]string{"error": "run_authority_denied"})
		return false
	}
	return true
}

func (r RunAPI) events(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req, "events") {
		return
	}
	after := int64(0)
	limit := 50
	var err error
	if raw := req.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_cursor"})
			return
		}
	}
	if raw := req.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
	}
	page, err := r.Store.RunEvents(req.Context(), req.PathValue("group"), req.PathValue("run"), after, limit)
	if errors.Is(err, projections.ErrNotFound) {
		write(w, 404, map[string]string{"error": "run_not_found"})
		return
	}
	if err != nil {
		write(w, 422, map[string]string{"error": "events_unavailable"})
		return
	}
	write(w, 200, page)
}
