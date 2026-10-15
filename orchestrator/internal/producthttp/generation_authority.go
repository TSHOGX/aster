package producthttp

import (
	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func (a *Authentication) generationInputGrant(w http.ResponseWriter, r *http.Request) {
	input, err := evolution.Decode[product.GenerationInputGrantInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
	if err != nil {
		writeError(w, 400, "invalid generation input grant")
		return
	}
	revision, err := a.Store.SetGenerationInputGrant(r.Context(), requestToken(r), r.PathValue("group"), input)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"revision": revision})
}
func (a *Authentication) identityGeneration(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if client.ID != "evaluation-space" && !(client.ID == "studio" && action == "result-authority") {
		writeError(w, 403, "generation service required")
		return
	}
	switch action {
	case "admit":
		input, err := evolution.Decode[evolution.GenerationAssignment](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil || input.Requester.Issuer != a.Config.Identity.Issuer {
			writeError(w, 400, "invalid generation assignment")
			return
		}
		grant, err := a.Store.AuthorizeGeneration(r.Context(), input)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, grant)
	case "authority":
		input, err := evolution.Decode[evolution.GenerationGrantScope](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeError(w, 400, "invalid generation scope")
			return
		}
		assignment, err := a.Store.ResolveGeneration(r.Context(), input)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, assignment)
	case "observe":
		input, err := evolution.Decode[evolution.GenerationObservation](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeError(w, 400, "invalid generation observation")
			return
		}
		if err = a.Store.ObserveGeneration(r.Context(), input); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	case "bind-result":
		input, err := evolution.Decode[struct {
			Grant evolution.GenerationGrant `json:"grant"`
			Scope evolution.RunResultScope  `json:"scope"`
		}](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeError(w, 400, "invalid generation result")
			return
		}
		if err = a.Store.BindGenerationResult(r.Context(), input.Grant, input.Scope); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	case "result-authority":
		scope, err := evolution.Decode[evolution.RunResultScope](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeError(w, 400, "invalid generation result scope")
			return
		}
		if err = a.Store.AuthorizeGenerationResult(r.Context(), scope); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	case "settle":
		input, err := evolution.Decode[product.GenerationSettlement](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			writeError(w, 400, "invalid generation settlement")
			return
		}
		if err = a.Store.SettleGeneration(r.Context(), input); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}
