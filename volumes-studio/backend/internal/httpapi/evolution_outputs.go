package httpapi

import (
	"aster/contracts/evolution"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"volume-studio/backend/internal/authoring"
)

type OutputReader interface {
	ResolveEvolutionOutput(context.Context, evolution.OutputCatalogScope) (evolution.Principal, error)
}
type EvolutionOutputAPI struct {
	Store  *authoring.Store
	Reader OutputReader
	secret [32]byte
}

func NewEvolutionOutputAPI(store *authoring.Store, reader OutputReader, secret string) (*EvolutionOutputAPI, error) {
	if store == nil || reader == nil || len(secret) < 32 {
		return nil, fmt.Errorf("output catalog dependencies required")
	}
	return &EvolutionOutputAPI{store, reader, sha256.Sum256([]byte(secret))}, nil
}
func (a *EvolutionOutputAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /volumes-studio/internal/v1/evolution/outputs", a.read)
	return mux
}
func (a *EvolutionOutputAPI) read(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if !ok || user != "central" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
		writeError(w, 403, "service_identity_required")
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, 415, "json_required")
		return
	}
	scope, err := evolution.Decode[evolution.OutputCatalogScope](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || scope.Query.Validate() != nil {
		writeError(w, 400, "invalid_output_scope")
		return
	}
	actor, err := a.Reader.ResolveEvolutionOutput(r.Context(), scope)
	if err != nil {
		writeError(w, 403, "output_authority_required")
		return
	}
	if actor.Kind != "human" || actor.Issuer == "" || actor.Subject == "" {
		writeError(w, 503, "output_authority_unavailable")
		return
	}
	var result any
	who := authoring.Actor{Issuer: actor.Issuer, Subject: actor.Subject}
	if scope.Query.LibraryID == "" {
		result, err = a.Store.EvolutionOutputCatalog(r.Context(), who, scope.Query)
	} else {
		result, err = a.Store.EvolutionOutputLibrary(r.Context(), who, scope.Query)
	}
	if err != nil {
		authoringError(w, err)
		return
	}
	if _, err = a.Reader.ResolveEvolutionOutput(r.Context(), scope); err != nil {
		writeError(w, 403, "output_authority_required")
		return
	}
	writeJSON(w, 200, result)
}
