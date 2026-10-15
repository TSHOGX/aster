package producthttp

import (
	"aster/contracts/evolution"
	"context"
	"harness-platform/orchestrator/internal/evaluationclient"
	"harness-platform/orchestrator/internal/product"
	"net/http"
	"strconv"
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
	if value.CentralValid && value.Configuration != nil {
		validator, ok := a.EvolutionCatalog.(interface {
			ValidateConfiguration(context.Context, evolution.Configuration) (evaluationclient.ConfigurationValidation, error)
		})
		if ok {
			validation, checkErr := validator.ValidateConfiguration(r.Context(), *value.Configuration)
			// Recheck current draft and authority after the participant read.
			current, currentErr := a.Store.PreviewEvolutionDraft(r.Context(), requestToken(r), r.PathValue("group"), input)
			if currentErr != nil {
				adminFailure(w, currentErr)
				return
			}
			if current.Digest != value.Digest {
				writeError(w, 409, "configuration preview changed")
				return
			}
			value = current
			valid := checkErr == nil && validation.Valid
			value.CatalogValid = &valid
			if checkErr != nil {
				value.CatalogError = "Evaluation Space catalog is unavailable. Retry without changing the selected revision."
			} else {
				value.CatalogError = validation.Error
			}
		} else {
			valid := false
			value.CatalogValid = &valid
			value.CatalogError = "Evaluation Space catalog connection is not configured."
		}
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
	value, err := a.Store.EvolutionOperation(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("operation"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (a *Authentication) evolutionOperationEvents(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	limit := 50
	var err error
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, 400, "invalid cursor")
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, "invalid limit")
			return
		}
	}
	value, err := a.Store.EvolutionOperationEvents(r.Context(), requestToken(r), r.PathValue("group"), r.PathValue("operation"), after, limit)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionConfigurationRevision(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("revision")
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != raw {
		writeError(w, 400, "invalid configuration revision")
		return
	}
	value, err := a.Store.EvolutionConfigurationRevision(r.Context(), requestToken(r), r.PathValue("group"), revision)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (a *Authentication) evolutionPermissions(w http.ResponseWriter, r *http.Request) {
	value, err := a.Store.EvolutionPermissions(r.Context(), requestToken(r), r.PathValue("group"))
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, value)
}
