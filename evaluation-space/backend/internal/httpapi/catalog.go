package httpapi

import (
	"aster/evaluation-space/internal/projections"
	"errors"
	"net/http"
	"strconv"
)

type CatalogAuthority interface {
	AuthorizeCatalog(*http.Request, string) error
}
type CatalogAPI struct {
	Store     *projections.Store
	Authority CatalogAuthority
}

func (a CatalogAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evaluation-space/api/v1/catalog/{kind}", a.list)
	return mux
}
func (a CatalogAPI) list(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	if a.Store == nil || a.Authority == nil || a.Authority.AuthorizeCatalog(r, r.PathValue("kind")) != nil {
		write(w, 403, map[string]string{"error": "catalog_authority_denied"})
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
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "catalog_not_found"})
		} else {
			write(w, 422, map[string]string{"error": "catalog_invalid"})
		}
		return
	}
	write(w, 200, page)
}
