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

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
)

// This listener accepts Studio's separately registered inbound credential only.
// Every action then resolves the exact current central grant before accessing data.
var ErrEvaluationDenied = errors.New("evaluation_authority_denied")

type EvaluationResolver interface {
	ResolveEvaluation(context.Context, evolution.EvaluationGrantScope) (evolution.EvaluationAssignment, error)
	AuthorizeEvaluationProfile(context.Context, evolution.EvaluationProfileScope) error
}
type EvaluationAPI struct {
	Store    *projections.Store
	Resolver EvaluationResolver
	secret   [32]byte
}

func NewEvaluationAPI(store *projections.Store, resolver EvaluationResolver, studioSecret string) (*EvaluationAPI, error) {
	if store == nil || resolver == nil || len(studioSecret) < 32 || len(studioSecret) > 1024 {
		return nil, fmt.Errorf("evaluation store, authority and Studio credential required")
	}
	return &EvaluationAPI{Store: store, Resolver: resolver, secret: sha256.Sum256([]byte(studioSecret))}, nil
}
func (a *EvaluationAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, action := range []string{"admit", "read", "result", "cancel", "profile", "verify"} {
		action := action
		mux.HandleFunc("POST /evaluation-space/api/v1/evaluations/"+action, func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, action) })
	}
	return mux
}
func (a *EvaluationAPI) serve(w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Cache-Control", "private, no-store")
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || !ok || user != "studio" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 {
		write(w, 403, map[string]string{"error": "service_identity_required"})
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return
	}
	if action == "profile" {
		scope, err := evolution.Decode[evolution.EvaluationProfileScope](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_profile_scope"})
			return
		}
		if err = a.Resolver.AuthorizeEvaluationProfile(r.Context(), scope); err != nil {
			write(w, 403, map[string]string{"error": "profile_authority_required"})
			return
		}
		raw, err := a.Store.CatalogDefinition(r.Context(), "profile", scope.Profile)
		if err != nil {
			a.reply(w, nil, err)
			return
		}
		profile, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
		if err != nil || profile.Dataset.GroupID != scope.GroupID {
			write(w, 403, map[string]string{"error": "profile_scope_mismatch"})
			return
		}
		a.reply(w, profile, nil)
		return
	}
	grant, err := evolution.Decode[evolution.EvaluationGrant](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || grant.Validate() != nil {
		write(w, 400, map[string]string{"error": "invalid_evaluation_grant"})
		return
	}
	scopeAction := "read"

	assignment, err := a.Resolver.ResolveEvaluation(r.Context(), evolution.EvaluationGrantScope{Grant: grant, Action: scopeAction})
	if err != nil {
		if errors.Is(err, ErrEvaluationDenied) {
			write(w, 403, map[string]string{"error": "evaluation_authority_required"})
		} else {
			write(w, 503, map[string]string{"error": "evaluation_authority_unavailable"})
		}
		return
	}
	if action == "admit" {
		existing, lookupErr := a.Store.Evaluation(r.Context(), grant.GroupID, grant.JobID)
		if lookupErr == nil {
			if existing.Grant == nil || *existing.Grant != grant {
				write(w, 409, map[string]string{"error": "evaluation_grant_mismatch"})
				return
			}
			a.reply(w, map[string]string{"id": existing.ID, "state": existing.State}, nil)
			return
		}
		if !errors.Is(lookupErr, projections.ErrNotFound) {
			a.reply(w, nil, lookupErr)
			return
		}
		assignment, err = a.Resolver.ResolveEvaluation(r.Context(), evolution.EvaluationGrantScope{Grant: grant, Action: "execute"})
		if err != nil {
			write(w, 403, map[string]string{"error": "evaluation_authority_required"})
			return
		}
		job, err := a.Store.AdmitAuthorizedEvaluation(r.Context(), grant, assignment)
		a.reply(w, map[string]string{"id": job.ID, "state": job.State}, err)
		return
	}
	job, err := a.Store.Evaluation(r.Context(), grant.GroupID, grant.JobID)
	if err != nil {
		a.reply(w, nil, err)
		return
	}
	if job.Grant == nil || *job.Grant != grant {
		write(w, 403, map[string]string{"error": "evaluation_grant_mismatch"})
		return
	}
	switch action {
	case "verify":
		state, err := a.Store.ServiceState(r.Context(), job.Service.ID)
		if err != nil || !state.Enabled {
			write(w, 403, map[string]string{"error": "evaluator_revoked"})
			return
		}
		a.reply(w, map[string]bool{"allowed": true}, nil)
	case "read":
		// No profile, dataset or report artifacts escape the metadata endpoint.
		budget, err := a.Store.EvaluationBudget(r.Context(), grant.GroupID, grant.JobID)
		a.reply(w, struct {
			ID     string                       `json:"id"`
			State  string                       `json:"state"`
			Fence  int64                        `json:"fence"`
			Budget projections.EvaluationBudget `json:"budget"`
		}{job.ID, job.State, job.Fence, budget}, err)
	case "result":
		result, err := a.Store.EvaluationResult(r.Context(), grant.GroupID, grant.JobID)
		a.reply(w, result, err)
	case "cancel":
		if job.State == "completed" || job.State == "error" || job.State == "canceled" {
			a.reply(w, map[string]string{"state": job.State}, nil)
			return
		}
		err := a.Store.CancelEvaluation(r.Context(), grant.GroupID, grant.JobID)
		a.reply(w, map[string]string{"state": "cancel_requested"}, err)
	}
}
func (a *EvaluationAPI) reply(w http.ResponseWriter, value any, err error) {
	if errors.Is(err, projections.ErrNotFound) {
		write(w, 404, map[string]string{"error": "evaluation_not_found"})
		return
	}
	if errors.Is(err, projections.ErrConflict) {
		write(w, 409, map[string]string{"error": "evaluation_conflict"})
		return
	}
	if err != nil {
		write(w, 422, map[string]string{"error": "evaluation_rejected"})
		return
	}
	write(w, 200, value)
}
