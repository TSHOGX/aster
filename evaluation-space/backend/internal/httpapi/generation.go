package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

type GenerationCentral interface {
	AuthorizeGenerationInput(context.Context, evolution.ArtifactRef) error
	AdmitGeneration(context.Context, evolution.GenerationAssignment) (evolution.GenerationGrant, error)
	ResolveGeneration(context.Context, evolution.GenerationGrantScope) (evolution.GenerationAssignment, error)
}
type GenerationAPI struct {
	Store   *projections.Store
	Central GenerationCentral
	secret  [32]byte
}

func NewGenerationAPI(store *projections.Store, central GenerationCentral, secret string) (*GenerationAPI, error) {
	if store == nil || central == nil || len(secret) < 32 || len(secret) > 1024 {
		return nil, fmt.Errorf("generation dependencies required")
	}
	return &GenerationAPI{store, central, sha256.Sum256([]byte(secret))}, nil
}
func (a *GenerationAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, action := range []string{"admit", "read", "control", "seal-input"} {
		action := action
		mux.HandleFunc("POST /evaluation-space/internal/v1/generation/"+action, func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, action) })
	}
	return mux
}
func (a *GenerationAPI) serve(w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Cache-Control", "private, no-store")
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if !ok || user != "central" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	if action == "seal-input" {
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			write(w, 415, map[string]string{"error": "binary_required"})
			return
		}
		encoded := r.Header.Get("X-Corpus-Artifact")
		if len(encoded) > 16384 {
			write(w, 400, map[string]string{"error": "invalid_input_reference"})
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_input_reference"})
			return
		}
		artifact, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(raw))
		if err != nil || artifact.Validate() != nil {
			write(w, 400, map[string]string{"error": "invalid_input_reference"})
			return
		}
		if err = a.Central.AuthorizeGenerationInput(r.Context(), artifact); err != nil {
			write(w, 403, map[string]string{"error": "input_authority_required"})
			return
		}
		err = a.Store.SealArtifact(r.Context(), artifact, http.MaxBytesReader(w, r.Body, projections.MaxStoredArtifactBytes))
		a.reply(w, artifact, err)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return
	}
	if action == "admit" {
		assignment, err := evolution.Decode[evolution.GenerationAssignment](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_assignment"})
			return
		}
		grant, err := a.Central.AdmitGeneration(r.Context(), assignment)
		if err != nil {
			write(w, 403, map[string]string{"error": "generation_authority_required"})
			return
		}
		run, err := a.Store.AdmitRun(r.Context(), projections.RunAdmission{ID: assignment.RunID, GroupID: assignment.GroupID, BindingID: assignment.BindingID, OccurrenceID: assignment.OccurrenceID, Pins: assignment.Pins})
		if err == nil {
			err = a.Store.RetainGenerationGrant(r.Context(), assignment, grant)
		}
		a.reply(w, run, err)
		return
	}
	input, err := evolution.Decode[struct {
		GroupID string                  `json:"group_id"`
		RunID   string                  `json:"run_id"`
		Control *projections.RunControl `json:"control,omitempty"`
	}](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		write(w, 400, map[string]string{"error": "invalid_run_scope"})
		return
	}
	run, err := a.Store.Run(r.Context(), input.GroupID, input.RunID)
	if err != nil {
		a.reply(w, nil, err)
		return
	}
	_, grant, err := a.Store.GenerationGrant(r.Context(), input.GroupID, input.RunID)
	if err != nil {
		a.reply(w, nil, err)
		return
	}
	if _, err = a.Central.ResolveGeneration(r.Context(), evolution.GenerationGrantScope{Grant: grant, Action: "read", Fence: run.Fence}); err != nil {
		write(w, 403, map[string]string{"error": "generation_authority_required"})
		return
	}
	if action == "read" {
		a.reply(w, struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Fence   int64  `json:"fence"`
			Handoff string `json:"handoff"`
			Branch  string `json:"branch"`
		}{run.ID, run.State, run.Fence, run.Handoff, run.ResultBranch}, nil)
		return
	}
	if input.Control == nil || input.Control.RunID != input.RunID {
		write(w, 400, map[string]string{"error": "invalid_run_control"})
		return
	}
	control := *input.Control
	control.GroupID = input.GroupID
	result, err := a.Store.ControlRunScoped(r.Context(), input.GroupID, control)
	a.reply(w, result, err)
}
func (a *GenerationAPI) reply(w http.ResponseWriter, result any, err error) {
	if errors.Is(err, projections.ErrNotFound) {
		write(w, 404, map[string]string{"error": "run_not_found"})
		return
	}
	if errors.Is(err, projections.ErrConflict) || errors.Is(err, projections.ErrBindingBusy) {
		write(w, 409, map[string]string{"error": "generation_conflict"})
		return
	}
	if err != nil {
		write(w, 422, map[string]string{"error": "generation_rejected"})
		return
	}
	write(w, 200, result)
}
