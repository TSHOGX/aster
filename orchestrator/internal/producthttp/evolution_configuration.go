package producthttp

import (
	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) evolutionDraft(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("group")
	if r.Method == http.MethodGet {
		value, err := a.Store.EvolutionDraft(r.Context(), requestToken(r), group)
		if err != nil {
			adminFailure(w, err)
			return
		}
		writeJSON(w, 200, value)
		return
	}
	input, err := evolution.Decode[product.EvolutionDraftInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil {
		writeError(w, 400, "invalid configuration draft")
		return
	}
	value, err := a.Store.SaveEvolutionDraft(r.Context(), requestToken(r), group, input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionGroupGrant(w http.ResponseWriter, r *http.Request) {
	var input product.EvolutionGroupGrantInput
	if !decode(w, r, 1<<16, &input) {
		return
	}
	revision, err := a.Store.SetEvolutionGroupGrant(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"revision": revision})
}

func (a *Authentication) evolutionConfiguration(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.EvolutionConfig(r.Context(), requestToken(r), r.PathValue("group"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) previewEvolution(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvolutionPreviewInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid preview request")
		return
	}
	value, err := a.Store.PreviewEvolutionDraft(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (a *Authentication) activateEvolution(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvolutionDraftActivationInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid activation request")
		return
	}
	value, err := a.Store.ActivateEvolutionDraft(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}
func (a *Authentication) disableEvolution(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.DisableEvolutionInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid disable request")
		return
	}
	value, err := a.Store.DisableEvolution(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionOperation(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.EvolutionOperation(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("operation"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) retryEvolutionOperation(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvolutionRetryInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid retry request")
		return
	}
	if err = a.Store.RetryEvolutionOperation(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("operation"), input); err != nil {
		adminFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Authentication) discardEvolutionOperation(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.EvolutionRetryInput](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeError(w, 400, "invalid discard request")
		return
	}
	if err = a.Store.DiscardEvolutionOperation(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("operation"), input); err != nil {
		adminFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
