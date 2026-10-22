package producthttp

import (
	"aster/contracts/evolution"
	"context"
	"errors"
	"harness-platform/orchestrator/internal/studioclient"
	"net/http"
	"strconv"
	"time"
)

type EvolutionOutputs interface {
	OutputCatalog(context.Context, evolution.OutputCatalogScope) (evolution.OutputCatalogPage, error)
	OutputLibrary(context.Context, evolution.OutputCatalogScope) (evolution.OutputLibraryDetail, error)
}

func (a *Authentication) evolutionOutputs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "invalid output limit")
			return
		}
		limit = n
	}
	query := evolution.OutputCatalogQuery{GroupID: r.PathValue("group"), LibraryID: r.PathValue("library"), Search: r.URL.Query().Get("search"), Cursor: r.URL.Query().Get("cursor"), Limit: limit}
	scope, err := a.Store.BeginEvolutionOutputRead(r.Context(), requestToken(r), query)
	if err != nil {
		adminFailure(w, err)
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		_ = a.Store.EndEvolutionOutputRead(ctx, scope.Ticket)
	}()
	if a.EvolutionOutputs == nil {
		writeJSON(w, 503, map[string]string{"code": "studio_unavailable", "error": "Studio output catalog connection is not configured."})
		return
	}
	var value any
	if query.LibraryID == "" {
		value, err = a.EvolutionOutputs.OutputCatalog(r.Context(), scope)
	} else {
		value, err = a.EvolutionOutputs.OutputLibrary(r.Context(), scope)
	}
	if _, authorityErr := a.Store.EvolutionOutputReader(r.Context(), scope); authorityErr != nil {
		adminFailure(w, authorityErr)
		return
	}
	if errors.Is(err, studioclient.ErrOutputDenied) {
		writeJSON(w, 403, map[string]string{"code": "output_denied", "error": "Current Studio library permission does not allow this read."})
		return
	}
	if errors.Is(err, studioclient.ErrNotFound) {
		writeError(w, 404, "output library not found")
		return
	}
	if errors.Is(err, studioclient.ErrOutputChanged) {
		writeJSON(w, 409, map[string]string{"code": "output_changed", "error": "The Studio catalog changed. Refresh the output list."})
		return
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"code": "studio_unavailable", "error": "Studio output metadata is unavailable. Refresh before choosing an output."})
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) identityOutputReader(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "studio" {
		writeError(w, 403, "Studio service required")
		return
	}
	scope, err := evolution.Decode[evolution.OutputCatalogScope](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid output scope")
		return
	}
	subject, err := a.Store.EvolutionOutputReader(r.Context(), scope)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, evolution.Principal{Issuer: a.Config.Identity.Issuer, Subject: subject, Kind: "human"})
}
