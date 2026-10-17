package producthttp

import (
	"context"
	"errors"
	"net/http"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/studioclient"
)

type manualBaseTransport interface {
	ManualBases(context.Context, studioclient.Projection) (map[string]evolution.ContentRef, error)
}

func (a *Authentication) evolutionManual(w http.ResponseWriter, r *http.Request) {
	var input product.EvolutionManualInput
	if !decode(w, r, 64<<10, &input) {
		return
	}
	group := r.PathValue("group")
	result, found, p, err := a.Store.BeginManualEvolution(r.Context(), requestToken(r), group, input)
	if err != nil {
		manualFailure(w, err)
		return
	}
	if found {
		writeJSON(w, 202, result)
		return
	}
	transport, ok := a.EvolutionOutputs.(manualBaseTransport)
	if !ok || a.Config.Identity == nil {
		writeJSON(w, 503, map[string]string{"error": "Evolution dispatch is not configured."})
		return
	}
	bases, err := transport.ManualBases(r.Context(), studioclient.Projection{OperationID: p.OperationID, Digest: p.Digest, Configuration: p.Configuration})
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Current output versions are unavailable. Retry the same request."})
		return
	}
	result, err = a.Store.StartManualEvolution(r.Context(), requestToken(r), group, a.Config.Identity.Issuer, input, bases)
	if err != nil {
		manualFailure(w, err)
		return
	}
	writeJSON(w, 202, result)
}

func manualFailure(w http.ResponseWriter, err error) {
	status, code, message := 500, "manual_run_unavailable", "Manual run admission is unavailable. Retry the same request."
	switch {
	case errors.Is(err, product.ErrForbidden):
		status, code, message = 403, "manual_run_denied", "Current group, source or output permissions do not allow this manual run."
	case errors.Is(err, product.ErrNotFound):
		status, code, message = 409, "manual_input_unavailable", "No eligible captured input is available, or the active configuration changed. Capture approved work or review the configuration."
	case errors.Is(err, product.ErrCapacity):
		status, code, message = 409, "manual_binding_busy", "A selected binding is busy or its input exceeds the spending ceiling. Wait for settlement or review the budget."
	case errors.Is(err, product.ErrInvalid):
		status, code, message = 422, "manual_selection_invalid", "Review selected workflows, prerequisite bindings and spending ceilings."
	case errors.Is(err, product.ErrConflict):
		status, code, message = 409, "manual_run_conflict", "The configuration or request identity changed. Recover the original request or review the current configuration."
	default:
		adminFailure(w, err)
		return
	}
	writeJSON(w, status, map[string]string{"code": code, "error": message})
}
