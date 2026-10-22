package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evolution-space/internal/examples"
	"aster/evolution-space/internal/modelconn"
	"aster/evolution-space/internal/projections"
)

// BrowserOptions carries operator registry facts into the browser surface.
// Models lists exact registered model revisions; nothing here carries secrets.
type BrowserOptions struct {
	Models          []evolution.Revision
	ExampleBuilders bool
	// Connections offers administrator-managed models; nil when disabled.
	Connections ModelConnections
}

// ModelConnections is the administrator-managed connection service. The
// interface keeps the browser API independent of the native runner.
type ModelConnections interface {
	Choices(context.Context) []modelconn.Choice
	List(context.Context) ([]modelconn.Connection, error)
	SealConfigured() bool
	Save(context.Context, string, string, modelconn.Input) (modelconn.Connection, error)
	StoredKey(context.Context, string) (string, error)
	Test(context.Context, modelconn.Settings, string) modelconn.TestResult
}

// ModelsEntitlement is issued only to platform administrators by central identity.
const ModelsEntitlement = "evolution_space.models"

type modelChoice struct {
	evolution.Revision
	Label string `json:"label,omitempty"`
	// CallTokens is the per-call reservation; zero when not reported.
	CallTokens int64 `json:"call_tokens,omitempty"`
}

// choices lists static models and the current revision of every enabled
// managed connection. Earlier revisions stay executable but are not offered.
func (o BrowserOptions) choices(ctx context.Context) []modelChoice {
	result := []modelChoice{}
	for _, ref := range o.Models {
		result = append(result, modelChoice{Revision: ref})
	}
	if o.Connections != nil {
		for _, choice := range o.Connections.Choices(ctx) {
			result = append(result, modelChoice{Revision: choice.Ref, Label: choice.Label, CallTokens: choice.CallTokens})
		}
	}
	return result
}

// previewModel stands in for a model in uninstalled example previews. It is a
// valid revision shape so the graph renders, and is never executable.
var previewModel = evolution.Revision{ID: "choose-a-model", Revision: 1, Digest: evolution.Digest([]byte("example-preview-model"))}

type exampleWorkflow struct {
	Ref       evolution.Revision `json:"ref"`
	Workflow  evolution.Workflow `json:"workflow"`
	NodeDocs  map[string]string  `json:"node_docs"`
	Installed bool               `json:"installed"`
}
type exampleSummary struct {
	examples.Starter
	Summary   string              `json:"summary"`
	Installed *evolution.Revision `json:"installed,omitempty"`
}
type exampleDetail struct {
	exampleSummary
	Workflows []exampleWorkflow   `json:"workflows"`
	Sample    examples.Sample     `json:"sample"`
	Model     *evolution.Revision `json:"model,omitempty"`
}

// BrowserExampleAPI exposes the built-in example catalog. Reading needs group
// workflow read access; installing publishes the fixed example definitions for
// one registered model and needs workflow edit access. It never activates a group.
func BrowserExampleAPI(client *appidentity.Client, store *projections.Store, options BrowserOptions) func(appidentity.Session, http.ResponseWriter, *http.Request) {
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
	registered := func(ctx context.Context, ref evolution.Revision) bool {
		for _, model := range options.choices(ctx) {
			if model.Revision == ref {
				return true
			}
		}
		return false
	}
	summary := func(r *http.Request, starter examples.Starter) (exampleSummary, error) {
		item := exampleSummary{Starter: starter, Summary: examples.Summary(starter.Template)}
		ref, err := store.LatestDefinition(r.Context(), "template", starter.Template)
		if err == nil {
			item.Installed = &ref
		} else if !errors.Is(err, projections.ErrNotFound) {
			return item, err
		}
		return item, nil
	}
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/models", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.read") {
			return
		}
		write(w, 200, map[string]any{"items": options.choices(r.Context()), "example_builders": options.ExampleBuilders, "connections": options.Connections != nil})
	})
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/examples", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.read") {
			return
		}
		items := []exampleSummary{}
		for _, starter := range examples.Starters() {
			item, err := summary(r, starter)
			if err != nil {
				write(w, 422, map[string]string{"error": "examples_unavailable"})
				return
			}
			items = append(items, item)
		}
		write(w, 200, map[string]any{"items": items, "example_builders": options.ExampleBuilders})
	})
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/examples/{template}", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.read") {
			return
		}
		var starter *examples.Starter
		for _, value := range examples.Starters() {
			if value.Template == r.PathValue("template") {
				starter = &value
				break
			}
		}
		if starter == nil {
			write(w, 404, map[string]string{"error": "example_not_found"})
			return
		}
		item, err := summary(r, *starter)
		if err != nil {
			write(w, 422, map[string]string{"error": "examples_unavailable"})
			return
		}
		detail := exampleDetail{exampleSummary: item, Workflows: []exampleWorkflow{}}
		model := previewModel
		if item.Installed != nil {
			// An installed example shows its exact published graphs and model.
			raw, err := store.CatalogDefinition(r.Context(), "template", *item.Installed)
			if err == nil {
				var template evolution.Template
				if json.Unmarshal(raw, &template) == nil && len(template.Workflows) > 0 {
					if root, err := store.Workflow(r.Context(), template.Workflows[0]); err == nil {
						for _, n := range root.Nodes {
							if n.ModelRef != nil {
								model = *n.ModelRef
								detail.Model = &model
								break
							}
						}
					}
				}
			}
		}
		packages, err := examples.Packages(model)
		if err != nil {
			write(w, 422, map[string]string{"error": "examples_unavailable"})
			return
		}
		for _, pkg := range packages {
			if pkg.Template.Ref.ID != starter.Template {
				continue
			}
			for _, published := range pkg.Workflows {
				_, installedErr := store.Workflow(r.Context(), published.Ref)
				detail.Workflows = append(detail.Workflows, exampleWorkflow{Ref: published.Ref, Workflow: published.Workflow, NodeDocs: examples.NodeDocs(published.Ref.ID), Installed: installedErr == nil})
			}
		}
		detail.Sample, _ = examples.SampleFor(starter.Template)
		write(w, 200, detail)
	})
	mux.HandleFunc("POST /evolution-space/api/v1/groups/{group}/examples/{template}/install", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, "workflow.edit") {
			return
		}
		input, err := evolution.Decode[struct {
			Model evolution.Revision `json:"model"`
		}](http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_install"})
			return
		}
		if !options.ExampleBuilders || !registered(r.Context(), input.Model) {
			write(w, 422, map[string]string{"error": "model_not_registered"})
			return
		}
		packages, err := examples.Packages(input.Model)
		if err != nil {
			write(w, 422, map[string]string{"error": "install_rejected"})
			return
		}
		for _, pkg := range packages {
			if pkg.Template.Ref.ID != r.PathValue("template") {
				continue
			}
			for _, published := range pkg.Workflows {
				raw, err := published.Workflow.Canonical()
				if err == nil {
					err = store.PublishWorkflow(r.Context(), published.Ref, raw)
				}
				if errors.Is(err, projections.ErrConflict) {
					// Revision 1 already exists for a different model.
					write(w, 409, map[string]string{"error": "installed_for_another_model"})
					return
				}
				if err != nil {
					write(w, 422, map[string]string{"error": "install_rejected"})
					return
				}
			}
			raw, _ := json.Marshal(pkg.Template)
			if err = store.PublishDefinition(r.Context(), "template", raw); errors.Is(err, projections.ErrConflict) {
				write(w, 409, map[string]string{"error": "installed_for_another_model"})
				return
			} else if err != nil {
				write(w, 422, map[string]string{"error": "install_rejected"})
				return
			}
			root := pkg.Workflows[len(pkg.Workflows)-1].Ref
			write(w, 201, map[string]any{"template": pkg.Template.Ref, "workflow": root})
			return
		}
		write(w, 404, map[string]string{"error": "example_not_found"})
	})
	// Datasets are group-owned evaluation inputs. Listing returns references
	// only, and both operations need configuration management, as profiles do.
	manage := func(w http.ResponseWriter, r *http.Request) bool {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		result, err := client.EvolutionSpaceGroups(r.Context(), session, appidentity.EvolutionSpaceAccessRequest{GroupID: r.PathValue("group"), Action: "config.manage", Limit: 1})
		if err == nil && (len(result.Items) != 1 || result.Items[0].ID != r.PathValue("group")) {
			err = appidentity.ErrUnauthorized
		}
		if err != nil {
			workflowAuthError(w, err)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}/datasets", func(w http.ResponseWriter, r *http.Request) {
		if !manage(w, r) {
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			if limit, err = strconv.Atoi(raw); err != nil {
				write(w, 400, map[string]string{"error": "invalid_limit"})
				return
			}
		}
		page, err := store.GroupDatasets(r.Context(), r.PathValue("group"), r.URL.Query().Get("after"), limit)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_dataset_page"})
			return
		}
		write(w, 200, page)
	})
	mux.HandleFunc("POST /evolution-space/api/v1/groups/{group}/datasets", func(w http.ResponseWriter, r *http.Request) {
		if !manage(w, r) {
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (media != "application/json" && media != "application/x-ndjson" && media != "text/csv" && media != "text/plain") {
			write(w, 415, map[string]string{"error": "unsupported_dataset_type"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, projections.MaxBrowserDatasetBytes+1))
		if err != nil || int64(len(body)) > projections.MaxBrowserDatasetBytes {
			write(w, 413, map[string]string{"error": "dataset_too_large"})
			return
		}
		if media == "application/json" && !json.Valid(body) {
			write(w, 422, map[string]string{"error": "invalid_json_dataset"})
			return
		}
		if media == "application/x-ndjson" {
			for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
				if len(bytes.TrimSpace(line)) > 0 && !json.Valid(line) {
					write(w, 422, map[string]string{"error": "invalid_jsonl_dataset"})
					return
				}
			}
		}
		ref, err := store.SealDataset(r.Context(), r.PathValue("group"), r.URL.Query().Get("id"), media, body)
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "dataset_conflict"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "dataset_rejected"})
			return
		}
		write(w, 201, ref)
	})
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}
