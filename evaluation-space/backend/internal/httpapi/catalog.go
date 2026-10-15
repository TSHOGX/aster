package httpapi

import (
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type CatalogScope struct {
	Kind   string              `json:"kind"`
	Action string              `json:"action"`
	Ref    *evolution.Revision `json:"ref,omitempty"`
}

var ErrCatalogDenied = errors.New("catalog_authority_denied")

type CatalogAuthority interface {
	AuthorizeCatalog(*http.Request, CatalogScope) error
}
type CatalogAPI struct {
	Store     *projections.Store
	Authority CatalogAuthority
}

func (a CatalogAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evaluation-space/api/v1/catalog/{kind}", a.list)
	mux.HandleFunc("GET /evaluation-space/api/v1/catalog/{kind}/{id}/{revision}", a.definition)
	return mux
}
func (a CatalogAPI) list(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r, CatalogScope{Kind: r.PathValue("kind"), Action: "list"}) {
		return
	}
	limit := 50
	afterID := r.URL.Query().Get("after_id")
	afterRevision := int64(0)
	var err error
	if raw := r.URL.Query().Get("after_revision"); raw != "" {
		afterRevision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_cursor"})
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
	}
	page, err := a.Store.CatalogRevisions(r.Context(), projections.CatalogListInput{Kind: r.PathValue("kind"), AfterID: afterID, AfterRevision: afterRevision, Limit: limit})
	if err != nil {
		if errors.Is(err, projections.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			write(w, 404, map[string]string{"error": "catalog_not_found"})
		} else {
			write(w, 422, map[string]string{"error": "catalog_invalid"})
		}
		return
	}
	write(w, 200, page)
}

func (a CatalogAPI) definition(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || rev < 1 || strconv.FormatInt(rev, 10) != r.PathValue("revision") {
		write(w, 400, map[string]string{"error": "invalid_revision"})
		return
	}
	digest := r.URL.Query().Get("digest")
	if len(digest) == 0 {
		write(w, 400, map[string]string{"error": "digest_required"})
		return
	}
	ref := evolution.Revision{ID: r.PathValue("id"), Revision: rev, Digest: digest}
	if ref.Validate() != nil {
		write(w, 400, map[string]string{"error": "invalid_definition_identity"})
		return
	}
	if !a.authorize(w, r, CatalogScope{Kind: r.PathValue("kind"), Action: "read", Ref: &ref}) {
		return
	}
	body, err := a.Store.CatalogDefinition(r.Context(), r.PathValue("kind"), evolution.Revision{ID: r.PathValue("id"), Revision: rev, Digest: digest})
	if err != nil {
		if errors.Is(err, projections.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			write(w, 404, map[string]string{"error": "definition_not_found"})
		} else {
			write(w, 409, map[string]string{"error": "definition_identity_conflict"})
		}
		return
	}
	write(w, 200, json.RawMessage(body))
}

func (a CatalogAPI) authorize(w http.ResponseWriter, r *http.Request, scope CatalogScope) bool {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return false
	}
	if a.Store == nil || a.Authority == nil {
		write(w, 503, map[string]string{"error": "catalog_authority_unavailable"})
		return false
	}
	if err := a.Authority.AuthorizeCatalog(r, scope); err != nil {
		if errors.Is(err, ErrCatalogDenied) {
			write(w, 403, map[string]string{"error": "catalog_authority_denied"})
		} else {
			write(w, 503, map[string]string{"error": "catalog_authority_unavailable"})
		}
		return false
	}
	return true
}
