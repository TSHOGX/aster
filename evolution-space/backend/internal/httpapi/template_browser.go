package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type templateAuthoringInput struct {
	Ref struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Digest   string `json:"digest,omitempty"`
	} `json:"ref"`
	Goal             string                     `json:"goal"`
	ParametersSchema json.RawMessage            `json:"parameters_schema"`
	Inputs           map[string]string          `json:"inputs"`
	Outputs          map[string]string          `json:"outputs"`
	Profile          string                     `json:"profile"`
	Workflows        []evolution.Revision       `json:"workflows"`
	SuggestedPrefix  string                     `json:"suggested_prefix"`
	Evaluation       evolution.EvaluationPolicy `json:"evaluation"`
}

// BrowserTemplateAPI exposes shared published metadata and group-owned authoring.
// It never activates a group or changes the operator-owned service registry.
func BrowserTemplateAPI(client *appidentity.Client, store *projections.Store) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	authorize := func(w http.ResponseWriter, r *http.Request, action string) bool {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		result, err := client.EvolutionSpaceWorkflowAccess(r.Context(), session, appidentity.EvolutionSpaceWorkflowAccessRequest{GroupID: r.PathValue("group"), Action: action})
		if err == nil && !result.Allowed {
			err = appidentity.ErrUnauthorized
		}
		if err != nil {
			workflowAuthError(w, err)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/templates", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.read") {
			return
		}
		var revision int64
		var err error
		if value := r.URL.Query().Get("after_revision"); value != "" {
			revision, err = strconv.ParseInt(value, 10, 64)
		}
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_cursor"})
			return
		}
		page, err := store.CatalogRevisions(r.Context(), projections.CatalogListInput{Kind: "template", AfterID: r.URL.Query().Get("after_id"), AfterRevision: revision, Limit: 50})
		if err != nil {
			write(w, 422, map[string]string{"error": "templates_unavailable"})
			return
		}
		write(w, 200, page)
	})
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/templates/{id}/{revision}", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.read") {
			return
		}
		ref, err := browserCatalogRef(r)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_template_reference"})
			return
		}
		body, err := store.CatalogDefinition(r.Context(), "template", ref)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "template_not_found"})
			return
		}
		if err != nil {
			write(w, 409, map[string]string{"error": "template_identity_conflict"})
			return
		}
		write(w, 200, json.RawMessage(body))
	})
	mux.HandleFunc("POST /evolution-space/api/v1/groups/{group}/templates", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.edit") {
			return
		}
		input, err := evolution.Decode[templateAuthoringInput](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_template"})
			return
		}
		value := evolution.Template{Ref: evolution.Revision{ID: input.Ref.ID, Revision: input.Ref.Revision}, Goal: input.Goal, ParametersSchema: input.ParametersSchema, Inputs: input.Inputs, Outputs: input.Outputs, Profile: input.Profile, Workflows: input.Workflows, SuggestedPrefix: input.SuggestedPrefix, Evaluation: input.Evaluation}
		value.Ref.Digest, err = evolution.DefinitionDigest(value)
		if err != nil {
			write(w, 422, map[string]string{"error": "template_rejected"})
			return
		}
		raw, err := json.Marshal(value)
		if err == nil {
			err = store.PublishOwnedTemplate(r.Context(), r.PathValue("group"), raw)
		}
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "template_revision_or_owner_conflict"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "template_rejected", "detail": err.Error()})
			return
		}
		write(w, 201, value.Ref)
	})
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}
