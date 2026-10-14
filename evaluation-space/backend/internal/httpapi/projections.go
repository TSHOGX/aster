// Package httpapi provides private Evaluation Space participant endpoints.
package httpapi

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

// ServiceAuthorizer verifies current central service identity and exact operation
// scope. Callers must implement this with installation authority, not body claims.
type ProjectionScope struct {
	GroupID     string
	OperationID string
	Revision    int64
	Digest      string
}
type ServiceAuthorizer interface {
	AuthorizeProjection(*http.Request, ProjectionScope) error
}
type ProjectionAPI struct {
	Store     *projections.Store
	Authority ServiceAuthorizer
}

func (a ProjectionAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /evaluation-space/api/v1/projections", a.adopt)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/projections/{revision}", a.lookup)
	return mux
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (a ProjectionAPI) adopt(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return
	}
	value, err := evolution.Decode[projections.Projection](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil {
		write(w, 400, map[string]string{"error": "invalid_projection"})
		return
	}
	if !a.authorize(w, r, ProjectionScope{GroupID: value.Configuration.GroupID, OperationID: value.OperationID, Revision: value.Configuration.Revision, Digest: value.Digest}) {
		return
	}
	if err = value.Configuration.Validate(); err != nil {
		write(w, 422, map[string]string{"error": "invalid_configuration"})
		return
	}
	raw, err := json.Marshal(value.Configuration)
	if err != nil || evolution.Digest(raw) != value.Digest {
		write(w, 422, map[string]string{"error": "projection_digest_mismatch"})
		return
	}
	if a.Store == nil {
		write(w, 503, map[string]string{"error": "projection_store_unavailable"})
		return
	}
	if err = a.Store.Adopt(r.Context(), value); err != nil {
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "projection_conflict"})
		} else {
			write(w, 503, map[string]string{"error": "projection_store_unavailable"})
		}
		return
	}
	// Adoption is a participant receipt, never a claim that central activation has settled.
	write(w, 200, map[string]string{"operation_id": value.OperationID, "configuration_digest": value.Digest, "revision": strconv.FormatInt(value.Configuration.Revision, 10), "state": "adopted"})
}

func (a ProjectionAPI) lookup(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	raw := r.PathValue("revision")
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != raw {
		write(w, 422, map[string]string{"error": "invalid_revision"})
		return
	}
	scope := ProjectionScope{GroupID: r.PathValue("group"), Revision: revision, OperationID: r.URL.Query().Get("operation_id"), Digest: r.URL.Query().Get("digest")}
	if !a.authorize(w, r, scope) {
		return
	}
	if a.Store == nil {
		write(w, 503, map[string]string{"error": "projection_store_unavailable"})
		return
	}
	projection, err := a.Store.Read(r.Context(), scope.GroupID, scope.Revision)
	if errors.Is(err, projections.ErrNotFound) {
		write(w, 404, map[string]string{"error": "projection_not_found"})
		return
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "projection_store_unavailable"})
		return
	}
	if projection.OperationID != scope.OperationID || projection.Digest != scope.Digest {
		write(w, 409, map[string]string{"error": "projection_conflict"})
		return
	}
	write(w, 200, map[string]string{"operation_id": projection.OperationID, "configuration_digest": projection.Digest, "revision": raw, "state": "adopted"})
}

var ErrAuthorityDenied = errors.New("projection_authority_denied")

func (a ProjectionAPI) authorize(w http.ResponseWriter, r *http.Request, scope ProjectionScope) bool {
	if a.Authority == nil {
		write(w, 503, map[string]string{"error": "authority_unavailable"})
		return false
	}
	if err := a.Authority.AuthorizeProjection(r, scope); err != nil {
		if errors.Is(err, ErrAuthorityDenied) {
			write(w, 403, map[string]string{"error": "projection_authority_required"})
		} else {
			write(w, 503, map[string]string{"error": "authority_unavailable"})
		}
		return false
	}
	return true
}
