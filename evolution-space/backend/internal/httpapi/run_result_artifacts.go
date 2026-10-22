package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
)

var ErrRunResultDenied = errors.New("run_result_authority_denied")

type RunResultResolver interface {
	AuthorizeRunResult(context.Context, evolution.RunResultScope) error
}
type RunResultArtifactAPI struct {
	Store     *projections.Store
	Authority RunResultResolver
	secret    [32]byte
}

func NewRunResultArtifactAPI(store *projections.Store, authority RunResultResolver, secret string) (*RunResultArtifactAPI, error) {
	if store == nil || authority == nil || len(secret) < 32 || len(secret) > 1024 {
		return nil, fmt.Errorf("run result artifact dependencies required")
	}
	return &RunResultArtifactAPI{Store: store, Authority: authority, secret: sha256.Sum256([]byte(secret))}, nil
}
func (a *RunResultArtifactAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /evolution-space/internal/v1/run-result-artifact", a.serve)
	return mux
}
func (a *RunResultArtifactAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if !ok || user != "studio" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return
	}
	input, err := evolution.Decode[struct {
		Scope    evolution.RunResultScope `json:"scope"`
		Artifact evolution.ArtifactRef    `json:"artifact"`
	}](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		write(w, 400, map[string]string{"error": "invalid_result_artifact"})
		return
	}
	if err = a.Authority.AuthorizeRunResult(r.Context(), input.Scope); err != nil {
		status := 503
		if errors.Is(err, ErrRunResultDenied) {
			status = 403
		}
		write(w, status, map[string]string{"error": "result_authority_required"})
		return
	}
	raw, err := a.Store.RunResultArtifact(r.Context(), input.Scope, input.Artifact)
	if err != nil {
		status := 409
		if errors.Is(err, projections.ErrNotFound) {
			status = 404
		} else if errors.Is(err, projections.ErrArtifactExpired) {
			status = 410
		}
		write(w, status, map[string]string{"error": "result_artifact_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Digest", input.Artifact.Digest)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": input.Artifact.ID}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, input.Artifact.ID, time.Time{}, bytes.NewReader(raw))
}
