package producthttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"harness-platform/orchestrator/internal/evolutionspaceclient"
)

type EvolutionCatalog interface {
	Catalog(context.Context, string, string, int64, int) (evolutionspaceclient.CatalogPage, error)
	CatalogDefinition(context.Context, string, evolution.Revision) (evolutionspaceclient.CatalogEntry, error)
}

func (a *Authentication) evolutionCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	group, kind := r.PathValue("group"), r.PathValue("kind")
	if !evolutionspaceclient.CatalogKind(kind) {
		writeError(w, 400, "invalid catalog kind")
		return
	}
	// The registered catalog is shared definition metadata; group config readers
	// may discover it. Reads confer no source, dataset, output or execute grant.
	if _, err := a.Store.EvolutionPermissions(r.Context(), requestToken(r), group); err != nil {
		adminFailure(w, err)
		return
	}
	if a.EvolutionCatalog == nil {
		writeJSON(w, 503, map[string]string{"code": "catalog_unavailable", "error": "Evolution Space catalog connection is not configured."})
		return
	}
	var result any
	var err error
	if id := r.PathValue("definition"); id != "" {
		revision, parseErr := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		ref := evolution.Revision{ID: id, Revision: revision, Digest: r.URL.Query().Get("digest")}
		if parseErr != nil || ref.Validate() != nil {
			writeError(w, 400, "invalid definition reference")
			return
		}
		result, err = a.EvolutionCatalog.CatalogDefinition(r.Context(), kind, ref)
	} else {
		revision := int64(0)
		limit := 25
		if raw := r.URL.Query().Get("after_revision"); raw != "" {
			revision, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeError(w, 400, "invalid catalog cursor")
				return
			}
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil {
				writeError(w, 400, "invalid catalog limit")
				return
			}
		}
		if after := r.URL.Query().Get("after_id"); after != "" && !manifest.ValidID(after) {
			writeError(w, 400, "invalid catalog cursor")
			return
		}
		if limit < 1 || limit > 50 || revision < 0 || (r.URL.Query().Get("after_id") == "") != (revision == 0) {
			writeError(w, 400, "invalid catalog page")
			return
		}
		result, err = a.EvolutionCatalog.Catalog(r.Context(), kind, r.URL.Query().Get("after_id"), revision, limit)
	}
	// An account/source revocation during a slow participant request must not leak
	// even a successful late response into the authenticated UI.
	if _, authorityErr := a.Store.EvolutionPermissions(r.Context(), requestToken(r), group); authorityErr != nil {
		adminFailure(w, authorityErr)
		return
	}
	if errors.Is(err, evolutionspaceclient.ErrNotFound) {
		writeError(w, 404, "catalog definition not found")
		return
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"code": "catalog_unavailable", "error": "Evolution Space catalog is unavailable. Retry without changing the selected revision."})
		return
	}
	writeJSON(w, 200, result)
}
