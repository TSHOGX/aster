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
	if a.Authority == nil || a.Authority.AuthorizeProjection(r, ProjectionScope{GroupID: value.Configuration.GroupID, OperationID: value.OperationID, Revision: value.Configuration.Revision, Digest: value.Digest}) != nil {
		write(w, 403, map[string]string{"error": "projection_authority_required"})
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
