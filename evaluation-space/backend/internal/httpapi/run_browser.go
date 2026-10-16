package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// BrowserRunAPI is the human trace boundary. It resolves the current group
// grant for every read and control request before returning durable projections.
// Worker RunAPI remains private and continues to require service identity.
func BrowserRunAPI(client *appidentity.Client, store *projections.Store) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	authorize := func(r *http.Request, session appidentity.Session, action string) error {
		result, err := client.EvaluationGroups(r.Context(), session, appidentity.EvaluationAccessRequest{GroupID: r.PathValue("group"), Action: action, Limit: 1})
		if err != nil {
			return err
		}
		if len(result.Items) != 1 || result.Items[0].ID != r.PathValue("group") {
			return appidentity.ErrUnauthorized
		}
		return nil
	}
	read := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "config.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		run, err := store.Run(r.Context(), r.PathValue("group"), r.PathValue("run"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "run_unavailable"})
			return
		}
		write(w, 200, run)
	}
	list := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "config.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if r.URL.Query().Get("limit") == "" {
			limit = 25
		}
		if err != nil || limit < 1 || limit > 50 {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
		page, err := store.Runs(r.Context(), r.PathValue("group"), r.URL.Query().Get("after"), limit)
		if err != nil {
			write(w, 422, map[string]string{"error": "runs_unavailable"})
			return
		}
		write(w, 200, page)
	}
	events := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		after, limit := int64(0), 50
		var err error
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseInt(raw, 10, 64)
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		if err != nil || after < 0 || limit < 1 || limit > 100 {
			write(w, 400, map[string]string{"error": "invalid_cursor"})
			return
		}
		page, err := store.RunEvents(r.Context(), r.PathValue("group"), r.PathValue("run"), after, limit)
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "events_unavailable"})
			return
		}
		write(w, 200, page)
	}
	attempts := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if r.URL.Query().Get("limit") == "" {
			limit = 50
		}
		if err != nil || limit < 1 || limit > 100 {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
		if _, err = store.Run(r.Context(), r.PathValue("group"), r.PathValue("run")); errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "run_unavailable"})
			return
		}
		page, err := store.WorkflowAttemptPage(r.Context(), r.PathValue("group"), r.PathValue("run"), r.URL.Query().Get("after"), limit)
		if err != nil {
			write(w, 422, map[string]string{"error": "attempts_unavailable"})
			return
		}
		write(w, 200, page)
	}
	attempt := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		record, err := store.WorkflowAttempt(r.Context(), r.PathValue("group"), r.PathValue("run"), r.PathValue("attempt"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "attempt_not_found"})
			return
		}
		if err != nil {
			write(w, 503, map[string]string{"error": "attempt_unavailable"})
			return
		}
		write(w, 200, record)
	}
	artifacts := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		limit := 100
		var err error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		if err != nil || limit < 1 || limit > 100 {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
		page, err := store.RunArtifacts(r.Context(), r.PathValue("group"), r.PathValue("run"), r.URL.Query().Get("after"), limit)
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "artifacts_unavailable"})
			return
		}
		write(w, 200, page)
	}
	artifact := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		ref, raw, err := store.RunArtifact(r.Context(), r.PathValue("group"), r.PathValue("run"), r.PathValue("artifact"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "artifact_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "artifact_unavailable"})
			return
		}
		maxBytes := int64(1 << 20)
		if value := r.URL.Query().Get("max_bytes"); value != "" {
			parsed, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || parsed < 1 || parsed > 4<<20 {
				write(w, 400, map[string]string{"error": "invalid_max_bytes"})
				return
			}
			maxBytes = parsed
		}
		truncated := int64(len(raw)) > maxBytes
		if truncated {
			raw = raw[:maxBytes]
			w.Header().Set("X-Artifact-Truncated", "true")
		} else {
			w.Header().Set("X-Artifact-Truncated", "false")
		}
		w.Header().Set("X-Artifact-Digest", ref.Digest)
		w.Header().Set("X-Artifact-Size", strconv.FormatInt(ref.Size, 10))
		w.Header().Set("Content-Type", ref.MediaType)
		w.Header().Set("Content-Disposition", "attachment; filename=\""+ref.ID+"\"")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, bytes.NewReader(raw))
	}
	workflow := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		run, err := store.Run(r.Context(), r.PathValue("group"), r.PathValue("run"))
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "run_unavailable"})
			return
		}
		graph, err := store.Workflow(r.Context(), run.Pins.Workflow)
		if err != nil {
			write(w, 422, map[string]string{"error": "workflow_unavailable"})
			return
		}
		write(w, 200, map[string]any{"revision": run.Pins.Workflow, "workflow": graph})
	}
	debugger := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "source.read"); err != nil {
			workflowAuthError(w, err)
			return
		}
		limit := 100
		var err error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		if err != nil || limit < 1 || limit > 100 {
			write(w, 400, map[string]string{"error": "invalid_limit"})
			return
		}
		value, err := store.RunDebugger(r.Context(), r.PathValue("group"), r.PathValue("run"), r.URL.Query().Get("after"), limit)
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "debugger_unavailable"})
			return
		}
		write(w, 200, value)
	}
	experimentPreview := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		for _, action := range []string{"run.start", "workflow.edit", "source.read"} {
			if err := authorize(r, session, action); err != nil {
				workflowAuthError(w, err)
				return
			}
		}
		value, err := evolution.Decode[projections.RunExperimentRequest](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil || value.SourceRunID != r.PathValue("run") || value.GroupID != "" && value.GroupID != r.PathValue("group") {
			write(w, 400, map[string]string{"error": "invalid_experiment_preview"})
			return
		}
		value.GroupID = r.PathValue("group")
		preview, err := store.PreviewRunExperiment(r.Context(), value)
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "run_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "experiment_preview_unavailable"})
			return
		}
		write(w, 200, preview)
	}
	experiment := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		for _, action := range []string{"run.start", "workflow.edit", "source.read"} {
			if err := authorize(r, session, action); err != nil {
				workflowAuthError(w, err)
				return
			}
		}
		value, err := evolution.Decode[projections.RunExperimentRequest](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil || value.SourceRunID != r.PathValue("run") || value.GroupID != "" && value.GroupID != r.PathValue("group") {
			write(w, 400, map[string]string{"error": "invalid_experiment_request"})
			return
		}
		value.GroupID = r.PathValue("group")
		result, err := store.CreateRunExperiment(r.Context(), value, evolution.Principal{Issuer: session.Issuer, Subject: session.Subject, Kind: "human"})
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "experiment_conflict"})
			return
		}
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "checkpoint_not_found"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "experiment_rejected", "detail": err.Error()})
			return
		}
		write(w, 201, result)
	}
	control := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "run.start"); err != nil {
			workflowAuthError(w, err)
			return
		}
		value, err := evolution.Decode[projections.RunControl](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil || value.RunID != r.PathValue("run") {
			write(w, 400, map[string]string{"error": "invalid_run_control"})
			return
		}
		value.GroupID = r.PathValue("group")
		result, err := store.ControlRunScoped(r.Context(), value.GroupID, value)
		if errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "run_control_conflict"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "run_control_rejected"})
			return
		}
		write(w, 200, result)
	}
	retry := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session, "run.start"); err != nil {
			workflowAuthError(w, err)
			return
		}
		value, err := evolution.Decode[projections.RunRetry](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil || value.SourceRunID != r.PathValue("run") || value.GroupID != "" && value.GroupID != r.PathValue("group") {
			write(w, 400, map[string]string{"error": "invalid_run_retry"})
			return
		}
		value.GroupID = r.PathValue("group")
		result, err := store.RetryRun(r.Context(), value, evolution.Principal{Issuer: session.Issuer, Subject: session.Subject, Kind: "human"})
		if errors.Is(err, projections.ErrConflict) || errors.Is(err, projections.ErrBindingBusy) {
			write(w, 409, map[string]string{"error": "run_retry_conflict"})
			return
		}
		if err != nil {
			write(w, 422, map[string]string{"error": "run_retry_rejected"})
			return
		}
		write(w, 201, result)
	}
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs", list)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}", read)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/events", events)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/attempts", attempts)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/attempts/{attempt}", attempt)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/artifacts", artifacts)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/artifacts/{artifact}", artifact)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/debugger", debugger)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/runs/{run}/workflow", workflow)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/runs/{run}/experiment-preview", experimentPreview)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/runs/{run}/experiment", experiment)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/runs/{run}/control", control)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/runs/{run}/retry", retry)
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}
