package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/workflow"
)

var ErrModelUnavailable = errors.New("native_model_unavailable")
var ErrModelDenied = errors.New("native_model_denied")
var ErrModelPending = errors.New("native_model_pending")

// ModelConnection is operator-owned. No field is resolved from prompts, node
// inputs, tool results or other model-controlled content.
type ModelConnection struct {
	Endpoint string
	ClientID string
	Secret   string
	Model    evolution.Revision
	Tools    []evolution.Revision
}
type HTTPModel struct {
	store      *projections.Store
	connection ModelConnection
	client     *http.Client
}
type ModelSubmission struct {
	RunID          string               `json:"run_id"`
	CallID         string               `json:"call_id"`
	IdempotencyKey string               `json:"idempotency_key"`
	RequestDigest  string               `json:"request_digest"`
	Model          evolution.Revision   `json:"model"`
	Call           workflow.Call        `json:"call"`
	Ceiling        projections.RunUsage `json:"ceiling"`
}

// ModelObservation identifies the exact retained call. Empty output is distinct
// from pending/failed/cancelled. Requested model and response-reported model name
// are separate; the latter never silently changes the requested revision.
type ModelObservation struct {
	RunID             string                 `json:"run_id"`
	CallID            string                 `json:"call_id"`
	IdempotencyKey    string                 `json:"idempotency_key"`
	RequestDigest     string                 `json:"request_digest"`
	Model             evolution.Revision     `json:"model"`
	ProviderRequestID string                 `json:"provider_request_id"`
	State             string                 `json:"state"`
	Usage             projections.RunUsage   `json:"usage"`
	Outputs           workflow.Values        `json:"outputs"`
	ResponseModel     string                 `json:"response_model"`
	ToolTraceState    string                 `json:"tool_trace_state"`
	Tools             []ModelToolObservation `json:"tools"`
}
type ModelToolObservation struct {
	ID     string             `json:"id"`
	Tool   evolution.Revision `json:"tool"`
	State  string             `json:"state"`
	Input  json.RawMessage    `json:"input"`
	Output json.RawMessage    `json:"output"`
}

func NewHTTPModel(store *projections.Store, connection ModelConnection) (*HTTPModel, error) {
	if store == nil || connection.Model.Validate() != nil || !manifest.ValidID(connection.ClientID) || len(connection.Secret) < 32 || len(connection.Secret) > 1024 {
		return nil, fmt.Errorf("registered model, store and credential required")
	}
	origin, err := url.Parse(connection.Endpoint)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, fmt.Errorf("canonical model-service origin required")
	}
	if origin.Scheme != "https" {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote model service requires HTTPS")
		}
	}
	tools := map[evolution.Revision]bool{}
	for _, tool := range connection.Tools {
		if tool.Validate() != nil || tools[tool] {
			return nil, fmt.Errorf("invalid registered model tools")
		}
		tools[tool] = true
	}
	connection.Tools = append([]evolution.Revision(nil), connection.Tools...)
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: time.Minute, MaxIdleConnsPerHost: 8}
	return &HTTPModel{store: store, connection: connection, client: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *HTTPModel) Close() { p.client.CloseIdleConnections() }
func (p *HTTPModel) frozen(ctx context.Context, call projections.ProviderCall) (workflow.Call, evolution.Run, error) {
	var node workflow.Call
	var run evolution.Run
	if !manifest.ValidID(call.RunID) || !manifest.ValidID(call.ID) || call.Request.Validate() != nil {
		return node, run, ErrModelDenied
	}
	key, _ := json.Marshal([]string{call.RunID, call.ID})
	if call.IdempotencyKey != evolution.Digest(key) {
		return node, run, ErrModelDenied
	}
	run, err := p.store.Run(ctx, call.Request.GroupID, call.RunID)
	if err != nil {
		return node, run, err
	}
	raw, err := p.store.Artifact(ctx, call.Request)
	if err != nil {
		return node, run, err
	}
	node, err = evolution.Decode[workflow.Call](bytes.NewReader(raw))
	if err != nil || node.ID != call.ID || node.Node.Kind != "agent" || node.Node.ModelRef == nil || *node.Node.ModelRef != p.connection.Model {
		return node, run, ErrModelDenied
	}
	allowed := map[evolution.Revision]bool{}
	for _, tool := range p.connection.Tools {
		allowed[tool] = true
	}
	for _, tool := range node.Node.Tools {
		if !allowed[tool] {
			return node, run, ErrModelDenied
		}
	}
	return node, run, nil
}
func (p *HTTPModel) Lookup(ctx context.Context, call projections.ProviderCall) (projections.ProviderOutcome, error) {
	node, run, err := p.frozen(ctx, call)
	if err != nil {
		return projections.ProviderOutcome{}, err
	}
	return p.call(ctx, http.MethodGet, "", call, node, run, nil, true)
}
func (p *HTTPModel) Submit(ctx context.Context, call projections.ProviderCall, raw []byte, ceiling projections.RunUsage) (projections.ProviderOutcome, error) {
	node, run, err := p.frozen(ctx, call)
	if err != nil {
		return projections.ProviderOutcome{}, err
	}
	if evolution.Digest(raw) != call.Request.Digest || int64(len(raw)) != call.Request.Size || ceiling.Tokens < 1 || ceiling.CostMicros < 0 || ceiling.Tokens > run.Budget.MaxTokens || ceiling.CostMicros > run.Budget.MaxCostMicros {
		return projections.ProviderOutcome{}, ErrModelDenied
	}
	submission := ModelSubmission{RunID: call.RunID, CallID: call.ID, IdempotencyKey: call.IdempotencyKey, RequestDigest: call.Request.Digest, Model: p.connection.Model, Call: node, Ceiling: ceiling}
	return p.call(ctx, http.MethodPut, "", call, node, run, submission, false)
}
func (p *HTTPModel) Cancel(ctx context.Context, call projections.ProviderCall) (projections.ProviderOutcome, error) {
	node, run, err := p.frozen(ctx, call)
	if err != nil {
		return projections.ProviderOutcome{}, err
	}
	input := struct {
		IdempotencyKey string             `json:"idempotency_key"`
		RequestDigest  string             `json:"request_digest"`
		Model          evolution.Revision `json:"model"`
	}{call.IdempotencyKey, call.Request.Digest, p.connection.Model}
	return p.call(ctx, http.MethodPost, "/cancel", call, node, run, input, false)
}
func (p *HTTPModel) call(ctx context.Context, method, suffix string, call projections.ProviderCall, node workflow.Call, run evolution.Run, input any, allowAbsent bool) (projections.ProviderOutcome, error) {
	var outcome projections.ProviderOutcome
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil || len(raw) > evolution.MaxDocumentBytes {
			return outcome, ErrModelDenied
		}
		body = bytes.NewReader(raw)
	}
	path := "/model/v1/runs/" + url.PathEscape(call.RunID) + "/calls/" + url.PathEscape(call.ID) + suffix
	request, err := http.NewRequestWithContext(ctx, method, p.connection.Endpoint+path, body)
	if err != nil {
		return outcome, ErrModelUnavailable
	}
	request.SetBasicAuth(p.connection.ClientID, p.connection.Secret)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", call.IdempotencyKey)
	request.Header.Set("X-Model-ID", p.connection.Model.ID)
	request.Header.Set("X-Model-Revision", fmt.Sprint(p.connection.Model.Revision))
	request.Header.Set("X-Model-Digest", p.connection.Model.Digest)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return outcome, ctx.Err()
		}
		return outcome, ErrModelUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return outcome, ErrModelDenied
	}
	if response.StatusCode == 404 && allowAbsent {
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1025))
		if err != nil || len(raw) > 1024 {
			return outcome, ErrModelUnavailable
		}
		missing, err := evolution.Decode[struct {
			Error string `json:"error"`
		}](bytes.NewReader(raw))
		if err == nil && missing.Error == "model_call_not_found" {
			return outcome, projections.ErrProviderCallNotFound
		}
		return outcome, ErrModelUnavailable
	}
	if response.StatusCode != 200 && response.StatusCode != 201 && response.StatusCode != 202 {
		return outcome, ErrModelUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return outcome, ErrModelUnavailable
	}
	observation, err := evolution.Decode[ModelObservation](bytes.NewReader(raw))
	if err != nil {
		return outcome, ErrModelUnavailable
	}
	if observation.RunID != call.RunID || observation.CallID != call.ID || observation.IdempotencyKey != call.IdempotencyKey || observation.RequestDigest != call.Request.Digest || observation.Model != p.connection.Model {
		return outcome, ErrModelDenied
	}
	if observation.State == "queued" || observation.State == "running" {
		return outcome, ErrModelPending
	}
	if observation.State != "completed" && observation.State != "failed" && observation.State != "cancelled" {
		return outcome, ErrModelUnavailable
	}
	if observation.ProviderRequestID == "" || len(observation.ProviderRequestID) > 512 || len(observation.ResponseModel) > 512 || observation.Usage.Tokens < 0 || observation.Usage.CostMicros < 0 {
		return outcome, ErrModelUnavailable
	}
	if err = validateToolObservations(node, observation); err != nil {
		return outcome, err
	}
	raw, err = json.Marshal(observation)
	if err != nil {
		return outcome, ErrModelUnavailable
	}
	// Bound and seal the authenticated observation, including omission status and
	// tool activity. Inspector reads still require the run's separate trace grant.
	if int64(len(raw)) > run.Budget.MaxArtifactBytes {
		return outcome, projections.ErrRunBudget
	}
	evidence := modelArtifact(call, "trace", raw, evolution.Digest([]byte("native-model-observation-v1")))
	outcome = projections.ProviderOutcome{CallID: call.ID, ProviderRequestID: observation.ProviderRequestID, State: observation.State, Usage: observation.Usage, Evidence: []evolution.ArtifactRef{evidence}}
	var output []byte
	if observation.State == "completed" {
		if observation.Outputs == nil {
			return projections.ProviderOutcome{}, ErrModelUnavailable
		}
		output, err = json.Marshal(observation.Outputs)
		if err != nil {
			return projections.ProviderOutcome{}, ErrModelUnavailable
		}
		if int64(len(raw)+len(output)) > run.Budget.MaxArtifactBytes {
			return projections.ProviderOutcome{}, projections.ErrRunBudget
		}
		schemas, _ := json.Marshal(node.Node.OutputPorts)
		artifact := modelArtifact(call, "output", output, evolution.Digest(schemas))
		outcome.Output = &artifact
	} else if len(observation.Outputs) != 0 {
		return projections.ProviderOutcome{}, ErrModelUnavailable
	}
	if err = p.store.SealArtifact(ctx, evidence, bytes.NewReader(raw)); err != nil {
		return projections.ProviderOutcome{}, err
	}
	if outcome.Output != nil {
		if err = p.store.SealArtifact(ctx, *outcome.Output, bytes.NewReader(output)); err != nil {
			return projections.ProviderOutcome{}, err
		}
	}
	return outcome, nil
}
func modelArtifact(call projections.ProviderCall, kind string, raw []byte, schema string) evolution.ArtifactRef {
	return evolution.ArtifactRef{ID: kind + "-" + strings.TrimPrefix(evolution.Digest([]byte(call.RunID+"\x00"+call.ID)), "sha256:"), GroupID: call.Request.GroupID, SchemaDigest: schema, Digest: evolution.Digest(raw), Size: int64(len(raw)), MediaType: "application/json", GrantID: call.Request.GrantID, PolicyRevision: call.Request.PolicyRevision}
}
func validateToolObservations(call workflow.Call, value ModelObservation) error {
	if value.ToolTraceState != "complete" && value.ToolTraceState != "omitted" {
		return ErrModelUnavailable
	}
	if len(value.Tools) > 1000 || value.ToolTraceState == "omitted" && len(value.Tools) != 0 {
		return ErrModelUnavailable
	}
	allowed := map[evolution.Revision]bool{}
	for _, tool := range call.Node.Tools {
		allowed[tool] = true
	}
	seen := map[string]bool{}
	for _, tool := range value.Tools {
		if !manifest.ValidID(tool.ID) || seen[tool.ID] || !allowed[tool.Tool] || (tool.State != "succeeded" && tool.State != "failed") {
			return ErrModelDenied
		}
		seen[tool.ID] = true
		for _, raw := range []json.RawMessage{tool.Input, tool.Output} {
			if _, err := evolution.Decode[json.RawMessage](bytes.NewReader(raw)); err != nil {
				return ErrModelUnavailable
			}
		}
	}
	return nil
}

var _ ModelProvider = (*HTTPModel)(nil)
