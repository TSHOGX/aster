package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"aster/contracts/evolution"
	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/identity"
)

type EvaluationInputResolver interface {
	ResolveEvaluationInput(context.Context, evolution.EvaluationGrant) (evolution.EvaluationAssignment, error)
}
type EvaluationInputAPI struct {
	Store    *authoring.Store
	Resolver EvaluationInputResolver
	secret   [32]byte
}

func NewEvaluationInputAPI(store *authoring.Store, resolver EvaluationInputResolver, secret string) (*EvaluationInputAPI, error) {
	if store == nil || resolver == nil || len(secret) < 32 || len(secret) > 1024 {
		return nil, fmt.Errorf("evaluation input authority and credential required")
	}
	return &EvaluationInputAPI{Store: store, Resolver: resolver, secret: sha256.Sum256([]byte(secret))}, nil
}
func (a *EvaluationInputAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /volumes-studio/internal/v1/evaluation-input", a.serve)
	return mux
}
func (a *EvaluationInputAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if !ok || user != "evolution-space" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 || r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
		writeError(w, 403, "service_identity_required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "json_required")
		return
	}
	input, err := evolution.Decode[struct {
		Grant evolution.EvaluationGrant `json:"grant"`
		Side  string                    `json:"side"`
		Path  string                    `json:"path"`
	}](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || input.Grant.Validate() != nil || (input.Side != "candidate" && input.Side != "reference") {
		writeError(w, 400, "invalid_evaluation_input")
		return
	}
	assignment, err := a.Resolver.ResolveEvaluationInput(r.Context(), input.Grant)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			writeError(w, 403, "evaluation_authority_required")
		} else {
			writeError(w, 503, "evaluation_authority_unavailable")
		}
		return
	}
	subject, _, err := assignment.Subject()
	if err != nil {
		writeError(w, 503, "invalid_evaluation_assignment")
		return
	}
	ref := subject.Candidate
	if input.Side == "reference" {
		if subject.Reference == nil {
			writeError(w, 404, "reference_not_requested")
			return
		}
		ref = *subject.Reference
	}
	actor := authoring.Actor{Issuer: assignment.Requester.Issuer, Subject: assignment.Requester.Subject}
	snapshot, err := a.Store.AssignedSnapshot(r.Context(), actor, ref)
	if err != nil {
		authoringError(w, err)
		return
	}
	if input.Path == "" {
		writeJSON(w, 200, struct {
			Reference evolution.ContentRef `json:"reference"`
			Entries   any                  `json:"entries"`
		}{ref, snapshot.Entries})
		return
	}
	reader, entry, err := a.Store.CommitContent(r.Context(), actor, ref.LibraryID, ref.CommitID, input.Path)
	if err != nil {
		authoringError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Path}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Content-Digest", entry.Digest)
	http.ServeContent(w, r, entry.Path, time.Time{}, reader)
}
