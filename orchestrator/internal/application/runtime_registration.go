package application

import (
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/runtimerelease"
	"net/http"
)

// RuntimeRegistration is accepted only on the protected operator socket. The
// owner supplies the engine store binding; neither the CLI nor browser selects it.
type RuntimeRegistration struct {
	Reference  product.ArtifactReference `json:"reference"`
	TemplateID string                    `json:"template_id"`
	Manifest   runtimerelease.Manifest   `json:"manifest"`
}

func (a *Application) registerRuntime(w http.ResponseWriter, r *http.Request) {
	var input RuntimeRegistration
	if !decodeControlLimit(w, r, &input, 2<<20) {
		return
	}
	if err := a.Backend.RequireTemplate(r.Context(), input.TemplateID); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	identity, err := a.Backend.Identity(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	err = a.Store.RegisterSandbox(r.Context(), product.RegisteredSandbox{Reference: input.Reference, StoreID: identity.StoreID, TemplateID: input.TemplateID, Manifest: input.Manifest})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeControl(w, map[string]any{"status": "registered", "reference": input.Reference, "default_changed": false})
}

func (a *Application) runtimeRegistryState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	value, err := a.Store.OperatorRegistryState(r.Context(), q.Get("kind"), product.ArtifactReference{ID: q.Get("id"), Version: q.Get("version"), Digest: q.Get("digest")})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeControl(w, value)
}
func (a *Application) editRuntimeRegistryState(w http.ResponseWriter, r *http.Request) {
	var input product.RegistryStateEdit
	if !decodeControl(w, r, &input) {
		return
	}
	value, err := a.Store.EditRegistryState(r.Context(), input)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeControl(w, value)
}
