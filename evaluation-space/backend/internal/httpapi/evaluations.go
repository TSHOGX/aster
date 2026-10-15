package httpapi

import (
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
	for _, action := range []string{"admit", "read", "result", "cancel"} {
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
	grant, err := evolution.Decode[evolution.EvaluationGrant](http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || grant.Validate() != nil {
		write(w, 400, map[string]string{"error": "invalid_evaluation_grant"})
		return
	}
	scopeAction := "read"
	if action == "admit" {
		scopeAction = "execute"
	}
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
