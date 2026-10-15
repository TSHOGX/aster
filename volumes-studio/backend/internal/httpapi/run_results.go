package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"mime"
	"net/http"

	"aster/contracts/evolution"
	"volume-studio/backend/internal/authoring"
)

type GenerationBaseResolver interface {
	ResolveGenerationBase(context.Context, evolution.GenerationGrantScope) (evolution.GenerationAssignment, error)
}
type RunResultAPI struct {
	BaseAuthority GenerationBaseResolver
	Store         *authoring.Store
	secret        [32]byte
}

func NewRunResultAPI(store *authoring.Store, secret string) (*RunResultAPI, error) {
	if store == nil || len(secret) < 32 || len(secret) > 1024 {
		return nil, fmt.Errorf("run-result store and service credential required")
	}
	return &RunResultAPI{Store: store, secret: sha256.Sum256([]byte(secret))}, nil
}
func (a *RunResultAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, action := range []string{"lookup", "submit", "base"} {
		action := action
		mux.HandleFunc("POST /volumes-studio/internal/v1/run-results/"+action, func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, action) })
	}
	return mux
}
func (a *RunResultAPI) serve(w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if !ok || user != "evaluation-space" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
		writeError(w, 403, "service_identity_required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "json_required")
		return
	}
	if action == "base" {
		scope, err := evolution.Decode[evolution.GenerationGrantScope](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil || scope.Action != "read" {
			writeError(w, 400, "invalid_generation_scope")
			return
		}
		if a.BaseAuthority == nil {
			writeError(w, 503, "generation_authority_unavailable")
			return
		}
		assignment, err := a.BaseAuthority.ResolveGenerationBase(r.Context(), scope)
		if err != nil {
			authoringError(w, err)
			return
		}
		tree, err := a.Store.AssignedSnapshot(r.Context(), authoring.Actor{Issuer: assignment.Requester.Issuer, Subject: assignment.Requester.Subject}, assignment.Pins.Base)
		respond(w, 200, struct {
			Reference evolution.ContentRef `json:"reference"`
			Entries   any                  `json:"entries"`
		}{assignment.Pins.Base, tree.Entries}, err)
		return
	}
	if action == "lookup" {
		scope, err := evolution.Decode[evolution.RunResultScope](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeError(w, 400, "invalid_run_result_scope")
			return
		}
		receipt, err := a.Store.RunResult(r.Context(), scope)
		respond(w, 200, receipt, err)
		return
	}
	input, err := evolution.Decode[struct {
		Scope evolution.RunResultScope `json:"scope"`
		Patch evolution.Patch          `json:"patch"`
	}](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil {
		writeError(w, 400, "invalid_run_result")
		return
	}
	receipt, err := a.Store.SubmitRunResult(r.Context(), input.Scope, input.Patch)
	respond(w, 200, receipt, err)
}
