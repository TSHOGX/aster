package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/studioclient"
)

type EvaluationContent interface {
	Snapshot(context.Context, evolution.EvaluationGrant, string, evolution.ContentRef) (studioclient.Snapshot, error)
	File(context.Context, evolution.EvaluationGrant, string, manifest.Entry) (io.ReadCloser, error)
}
type EvaluationInputAPI struct {
	Store       *projections.Store
	Resolver    EvaluationResolver
	Content     EvaluationContent
	credentials map[string][32]byte
	slots       chan struct{}
}

func NewEvaluationInputAPI(store *projections.Store, resolver EvaluationResolver, content EvaluationContent, credentials map[string]string) (*EvaluationInputAPI, error) {
	if store == nil || resolver == nil {
		return nil, fmt.Errorf("provider input store and authority required")
	}
	hashed := map[string][32]byte{}
	for id, secret := range credentials {
		if !manifest.ValidID(id) || len(secret) < 32 || len(secret) > 1024 {
			return nil, fmt.Errorf("invalid provider input credential")
		}
		hashed[id] = sha256.Sum256([]byte(secret))
	}
	return &EvaluationInputAPI{Store: store, Resolver: resolver, Content: content, credentials: hashed, slots: make(chan struct{}, 4)}, nil
}
func (a *EvaluationInputAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /evolution-space/provider/v1/groups/{group}/jobs/{job}/{kind}", a.serve)
	return mux
}
func (a *EvaluationInputAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	connection, password, ok := r.BasicAuth()
	expected, registered := a.credentials[connection]
	actual := sha256.Sum256([]byte(password))
	if !ok || !registered || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		write(w, 403, map[string]string{"error": "provider_identity_required"})
		return
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		write(w, 429, map[string]string{"error": "input_busy"})
		return
	}
	rawFence := r.Header.Get("X-Evaluation-Fence")
	fence, err := strconv.ParseInt(rawFence, 10, 64)
	if err != nil || fence < 1 || strconv.FormatInt(fence, 10) != rawFence {
		write(w, 400, map[string]string{"error": "invalid_dispatch_fence"})
		return
	}
	job, err := a.Store.EvaluationInput(r.Context(), r.PathValue("group"), r.PathValue("job"), r.Header.Get("X-Evaluation-Grant"), fence)
	if err != nil {
		write(w, 403, map[string]string{"error": "input_scope_denied"})
		return
	}
	raw, err := a.Store.CatalogDefinition(r.Context(), "service", job.Service)
	if err != nil {
		write(w, 503, map[string]string{"error": "provider_definition_unavailable"})
		return
	}
	service, err := evolution.Decode[evolution.Service](bytes.NewReader(raw))
	if err != nil || service.ConnectionID != connection || service.Kind != "evaluation" {
		write(w, 403, map[string]string{"error": "input_provider_mismatch"})
		return
	}
	state, err := a.Store.ServiceState(r.Context(), job.Service.ID)
	if err != nil || !state.Enabled {
		write(w, 403, map[string]string{"error": "provider_revoked"})
		return
	}
	if _, err = a.Resolver.ResolveEvaluation(r.Context(), evolution.EvaluationGrantScope{Grant: *job.Grant, Action: "input"}); err != nil {
		code := 503
		if errors.Is(err, ErrEvaluationDenied) {
			code = 403
		}
		write(w, code, map[string]string{"error": "input_authority_unavailable"})
		return
	}
	kind := r.PathValue("kind")
	if kind == "dataset" {
		data, err := a.Store.Artifact(r.Context(), job.Subject.Dataset)
		if err != nil {
			write(w, 503, map[string]string{"error": "dataset_unavailable"})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": job.Subject.Dataset.ID}))
		w.Header().Set("X-Content-Digest", job.Subject.Dataset.Digest)
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		http.ServeContent(w, r, job.Subject.Dataset.ID, time.Time{}, bytes.NewReader(data))
		return
	}
	if kind != "candidate" && kind != "reference" {
		write(w, 404, map[string]string{"error": "input_not_found"})
		return
	}
	ref := job.Subject.Candidate
	if kind == "reference" {
		if job.Subject.Reference == nil {
			write(w, 404, map[string]string{"error": "reference_not_requested"})
			return
		}
		ref = *job.Subject.Reference
	}
	if a.Content == nil {
		write(w, 503, map[string]string{"error": "Studio_input_unavailable"})
		return
	}
	snapshot, err := a.Content.Snapshot(r.Context(), *job.Grant, kind, ref)
	if err != nil {
		write(w, 503, map[string]string{"error": "snapshot_unavailable"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		write(w, 200, snapshot)
		return
	}
	var entry manifest.Entry
	for _, item := range snapshot.Entries {
		if item.Path == path {
			entry = item
			break
		}
	}
	if entry.Kind != "file" {
		write(w, 404, map[string]string{"error": "input_file_not_found"})
		return
	}
	reader, err := a.Content.File(r.Context(), *job.Grant, kind, entry)
	if err != nil {
		write(w, 503, map[string]string{"error": "input_file_unavailable"})
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", entry.Size)
	w.Header().Set("X-Content-Digest", entry.Digest)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Path}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	size, _ := manifest.ParseSize(entry.Size)
	_, _ = io.CopyN(w, reader, size)
}
