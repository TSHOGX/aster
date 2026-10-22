package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/modelconn"
	"aster/evolution-space/internal/projections"
	"aster/evolution-space/internal/runner"
	"aster/evolution-space/internal/workflow"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// SchemaLookup returns a registered self-contained JSON Schema by digest.
type SchemaLookup func(digest string) (json.RawMessage, bool)

// Adapter runs native agent calls against managed connections. Each call is
// recorded before dispatch, so lookup after a restart can never resubmit it.
type Adapter struct {
	Store       *modelconn.Store
	Projections *projections.Store
	Schemas     SchemaLookup
	client      *modelconn.Client
	mu          sync.Mutex
	inflight    map[string]*inflight
	slots       map[string]chan struct{}
	providers   map[evolution.Revision]*runner.HTTPModel
	compiled    map[string]*jsonschema.Schema
}
type inflight struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewAdapter(store *modelconn.Store, projectionStore *projections.Store, schemas SchemaLookup) *Adapter {
	return &Adapter{Store: store, Projections: projectionStore, Schemas: schemas, inflight: map[string]*inflight{}, slots: map[string]chan struct{}{}, providers: map[evolution.Revision]*runner.HTTPModel{}, compiled: map[string]*jsonschema.Schema{},
		client: modelconn.NewClient()}
}

// Model implements runner.ModelSource for every pinned connection revision.
func (a *Adapter) Model(ctx context.Context, ref evolution.Revision) (runner.Model, bool) {
	connection, _, err := a.Store.Revision(ctx, ref)
	if err != nil {
		return runner.Model{}, false
	}
	a.mu.Lock()
	provider := a.providers[ref]
	if provider == nil {
		provider, err = runner.NewExchangeModel(a.Projections, ref, a)
		if err != nil {
			a.mu.Unlock()
			return runner.Model{}, false
		}
		a.providers[ref] = provider
	}
	a.mu.Unlock()
	return runner.Model{Provider: provider, Ceiling: projections.RunUsage{Tokens: connection.Policy.CallTokens, CostMicros: connection.Policy.CostCeiling()}, Tools: map[evolution.Revision]bool{},
		Disabled: !connection.Enabled || !connection.KeySet || !a.Store.SealConfigured()}, true
}

func (a *Adapter) Choices(ctx context.Context) []modelconn.Choice {
	connections, err := a.Store.List(ctx)
	if err != nil {
		return nil
	}
	result := []modelconn.Choice{}
	for _, c := range connections {
		if c.Enabled && c.KeySet && a.Store.SealConfigured() {
			result = append(result, modelconn.Choice{Ref: c.Current, Label: c.Name + " · " + c.Settings.Model, CallTokens: c.Policy.CallTokens})
		}
	}
	return result
}

func callKey(key runner.ModelCallKey) string { return key.RunID + "\x00" + key.CallID }
func storeKey(key runner.ModelCallKey) modelconn.CallKey {
	return modelconn.CallKey{RunID: key.RunID, CallID: key.CallID, IdempotencyKey: key.IdempotencyKey, RequestDigest: key.RequestDigest, Model: key.Model}
}
func (a *Adapter) finish(ctx context.Context, key runner.ModelCallKey, instance string, value runner.ModelObservation) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, runner.ErrModelUnavailable
	}
	raw, err = a.Store.Finish(ctx, storeKey(key), instance, value.State, raw)
	if err != nil {
		return nil, runner.ErrModelUnavailable
	}
	return raw, nil
}
func (a *Adapter) terminal(ctx context.Context, key runner.ModelCallKey, value runner.ModelObservation) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, runner.ErrModelUnavailable
	}
	raw, err = a.Store.InsertTerminal(ctx, storeKey(key), value.State, raw)
	if err != nil {
		return nil, runner.ErrModelUnavailable
	}
	return raw, nil
}

func (a *Adapter) Lookup(ctx context.Context, key runner.ModelCallKey) ([]byte, error) {
	row, err := a.Store.Call(ctx, storeKey(key))
	if errors.Is(err, modelconn.ErrCallNotFound) {
		return nil, projections.ErrProviderCallNotFound
	}
	if err != nil {
		return nil, runner.ErrModelUnavailable
	}
	return a.await(ctx, key, row)
}

// await returns a terminal observation, waits for a call running here, or
// settles a call left running by a previous process as interrupted.
func (a *Adapter) await(ctx context.Context, key runner.ModelCallKey, row modelconn.CallRow) ([]byte, error) {
	if row.RequestDigest != key.RequestDigest || row.IdempotencyKey != key.IdempotencyKey || row.Model != modelconn.RefString(key.Model) {
		return nil, runner.ErrModelDenied
	}
	if row.State != "running" {
		return row.Observation, nil
	}
	a.mu.Lock()
	active := a.inflight[callKey(key)]
	a.mu.Unlock()
	if active != nil && row.Instance == a.Store.Instance() {
		select {
		case <-active.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		row, err := a.Store.Call(ctx, storeKey(key))
		if err != nil {
			return nil, runner.ErrModelUnavailable
		}
		return row.Observation, nil
	}
	// The dispatching process ended before recording a result. The provider may
	// have spent up to the ceiling; report that conservatively and allow retry.
	value := observation(key, "failed")
	value.Retryable = true
	value.Usage = projections.RunUsage{Tokens: row.CeilingTokens, CostMicros: row.CeilingCost}
	value.Error = "interrupted: the service restarted during this call; usage is counted at the reserved ceiling"
	return a.finish(ctx, key, row.Instance, value)
}

func (a *Adapter) Cancel(ctx context.Context, key runner.ModelCallKey) ([]byte, error) {
	row, err := a.Store.Call(ctx, storeKey(key))
	if errors.Is(err, modelconn.ErrCallNotFound) {
		// Never dispatched: record the cancellation with no spend.
		value := observation(key, "cancelled")
		value.Error = "cancelled before dispatch"
		return a.terminal(ctx, key, value)
	}
	if err != nil {
		return nil, runner.ErrModelUnavailable
	}
	if row.State == "running" {
		a.mu.Lock()
		active := a.inflight[callKey(key)]
		a.mu.Unlock()
		if active != nil {
			active.cancel()
		}
	}
	return a.await(ctx, key, row)
}

func (a *Adapter) Submit(ctx context.Context, key runner.ModelCallKey, submission runner.ModelSubmission) ([]byte, error) {
	if submission.RunID != key.RunID || submission.CallID != key.CallID || submission.IdempotencyKey != key.IdempotencyKey || submission.RequestDigest != key.RequestDigest || submission.Model != key.Model {
		return nil, runner.ErrModelDenied
	}
	if row, err := a.Store.Call(ctx, storeKey(key)); err == nil {
		return a.await(ctx, key, row)
	} else if !errors.Is(err, modelconn.ErrCallNotFound) {
		return nil, runner.ErrModelUnavailable
	}
	connection, settings, apiKey, err := a.Store.Resolve(ctx, key.Model)
	if err != nil && !errors.Is(err, modelconn.ErrNoKey) && !errors.Is(err, modelconn.ErrNoSealKey) {
		return nil, runner.ErrModelDenied
	}
	if err != nil || !connection.Enabled {
		value := observation(key, "failed")
		value.Error = "model connection is disabled or has no usable API key"
		return a.terminal(ctx, key, value)
	}
	work, cancel := context.WithTimeout(ctx, time.Duration(connection.Policy.TimeoutSeconds)*time.Second)
	defer cancel()
	active := &inflight{cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.inflight[callKey(key)] != nil {
		a.mu.Unlock()
		return nil, runner.ErrModelPending
	}
	if err = a.Store.InsertRunning(ctx, storeKey(key), submission.Ceiling.Tokens, submission.Ceiling.CostMicros); err != nil {
		a.mu.Unlock()
		if errors.Is(err, modelconn.ErrConflict) {
			if row, readErr := a.Store.Call(ctx, storeKey(key)); readErr == nil {
				return a.await(ctx, key, row)
			}
		}
		return nil, runner.ErrModelUnavailable
	}
	a.inflight[callKey(key)] = active
	slot := a.slots[connection.ID]
	if slot == nil || cap(slot) != int(connection.Policy.Concurrency) {
		slot = make(chan struct{}, connection.Policy.Concurrency)
		a.slots[connection.ID] = slot
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.inflight, callKey(key))
		a.mu.Unlock()
		close(active.done)
	}()
	var value runner.ModelObservation
	select {
	case slot <- struct{}{}:
		value = a.execute(work, key, submission, connection, settings, apiKey)
		<-slot
	case <-work.Done():
		value = observation(key, "cancelled")
		value.Error = "cancelled while waiting for a free connection slot"
	}
	// Record with a context that outlives cancellation of the call itself.
	record, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	return a.finish(record, key, a.Store.Instance(), value)
}

func observation(key runner.ModelCallKey, state string) runner.ModelObservation {
	return runner.ModelObservation{RunID: key.RunID, CallID: key.CallID, IdempotencyKey: key.IdempotencyKey, RequestDigest: key.RequestDigest, Model: key.Model,
		ProviderRequestID: "local-" + strings.TrimPrefix(evolution.Digest([]byte(key.RunID+"\x00"+key.CallID)), "sha256:")[:24], State: state, Outputs: workflow.Values{}, ToolTraceState: "complete", Tools: []runner.ModelToolObservation{}}
}

// estimate over-counts tokens (about three bytes each) so that a reserved
// ceiling is not exceeded by the prompt itself.
func estimate(messages []modelconn.Message) int64 {
	var size int64
	for _, m := range messages {
		size += int64(len(m.Content)) + 8
	}
	return size/3 + 16
}

func (a *Adapter) execute(ctx context.Context, key runner.ModelCallKey, submission runner.ModelSubmission, connection modelconn.Connection, settings modelconn.Settings, apiKey string) runner.ModelObservation {
	value := observation(key, "failed")
	node := submission.Call.Node
	if len(node.Tools) != 0 || node.Kind != "agent" {
		value.Error = "model connections do not run tools"
		return value
	}
	ports := make([]string, 0, len(node.OutputPorts))
	for name := range node.OutputPorts {
		ports = append(ports, name)
	}
	sort.Strings(ports)
	var contract strings.Builder
	contract.WriteString("You are one step in an Aster workflow. Reply with exactly one JSON object and nothing else: no Markdown fences, no commentary. ")
	contract.WriteString("Its keys must be exactly: " + strings.Join(ports, ", ") + ". Each value must satisfy its JSON Schema.\n")
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range ports {
		raw, ok := a.Schemas(node.OutputPorts[name])
		compiled, err := a.compile(node.OutputPorts[name], raw)
		if !ok || err != nil {
			value.Error = "output schema for port " + name + " is not registered"
			return value
		}
		schemas[name] = compiled
		contract.WriteString("\nSchema for \"" + name + "\":\n" + string(raw) + "\n")
	}
	messages := []modelconn.Message{{Role: "system", Content: contract.String()}, {Role: "user", Content: submission.Call.Prompt}}
	var used projections.RunUsage
	responseModel, requestID, lastError := "", "", ""
	for attempt := 0; attempt < 2; attempt++ {
		budget := submission.Ceiling.Tokens - used.Tokens - estimate(messages)
		if settings.MaxOutputTokens < budget {
			budget = settings.MaxOutputTokens
		}
		if price := connection.Policy.OutputMicrosPerMillion; price > 0 {
			// The prompt is billed at the input rate; only the rest pays for output.
			prompt := (estimate(messages)*connection.Policy.InputMicrosPerMillion + 999_999) / 1_000_000
			if byCost := (submission.Ceiling.CostMicros - used.CostMicros - prompt) * 1_000_000 / price; byCost < budget {
				budget = byCost
			}
		}
		if budget < 256 {
			if attempt == 0 {
				value.Error = "prompt_exceeds_call_ceiling: raise the connection's call token ceiling or shorten the input"
			} else {
				value.Error = lastError
				value.Retryable = true
			}
			break
		}
		reply, status, err := a.client.Complete(ctx, settings, apiKey, messages, budget)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				value.State = "cancelled"
				value.Error = "cancelled during the model call; usage is counted at the reserved ceiling"
			} else {
				value.Retryable = true
				value.Error = "model service unreachable or timed out; usage is counted at the reserved ceiling"
			}
			used = projections.RunUsage{Tokens: submission.Ceiling.Tokens, CostMicros: submission.Ceiling.CostMicros}
			break
		}
		if status != 200 {
			value.Retryable = status == 429 || status >= 500
			value.Error = fmt.Sprintf("model service returned HTTP %d", status)
			if status == 401 || status == 403 {
				value.Error += ": check the connection's API key"
			}
			break
		}
		usage := reply.Usage.TotalTokens
		if usage < reply.Usage.PromptTokens+reply.Usage.CompletionTokens {
			usage = reply.Usage.PromptTokens + reply.Usage.CompletionTokens
		}
		if usage == 0 {
			usage = estimate(messages) + budget
		}
		used.Tokens += usage
		used.CostMicros += (reply.Usage.PromptTokens*connection.Policy.InputMicrosPerMillion + reply.Usage.CompletionTokens*connection.Policy.OutputMicrosPerMillion + 999_999) / 1_000_000
		responseModel, requestID = reply.Model, reply.ID
		if len(reply.Choices) == 0 {
			value.Retryable, lastError = true, "model returned no choices"
			value.Error = lastError
			continue
		}
		content, finish := reply.Choices[0].Message.Content, reply.Choices[0].FinishReason
		outputs, err := parse(content, ports, schemas)
		if err == nil {
			value.State, value.Outputs, value.Error, value.Retryable = "completed", outputs, "", false
			break
		}
		lastError = "invalid_model_output: " + err.Error()
		if finish == "length" {
			lastError = "output_truncated: raise the connection's maximum output tokens"
		}
		value.Retryable, value.Error = true, modelconn.Bound(lastError)
		messages = append(messages, modelconn.Message{Role: "assistant", Content: modelconn.Bound(content)}, modelconn.Message{Role: "user", Content: "That reply was rejected: " + modelconn.Bound(err.Error()) + ". Reply again with only the corrected JSON object."})
	}
	if used.Tokens > submission.Ceiling.Tokens || used.CostMicros > submission.Ceiling.CostMicros {
		// The provider spent more than its estimate allowed. Record the ceiling,
		// which the run budget has already reserved, and say so in evidence.
		value.Error = modelconn.Bound(strings.TrimSpace(value.Error + fmt.Sprintf(" usage_over_ceiling: reported %d tokens, %d micros", used.Tokens, used.CostMicros)))
		used = projections.RunUsage{Tokens: min(used.Tokens, submission.Ceiling.Tokens), CostMicros: min(used.CostMicros, submission.Ceiling.CostMicros)}
	}
	value.Usage = used
	value.ResponseModel = modelconn.Bound(responseModel)
	if requestID != "" && len(requestID) <= 256 {
		value.ProviderRequestID = requestID
	}
	if value.State != "completed" {
		value.Outputs = workflow.Values{}
	}
	return value
}

func (a *Adapter) compile(digest string, raw json.RawMessage) (*jsonschema.Schema, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if schema := a.compiled[digest]; schema != nil {
		return schema, nil
	}
	if evolution.Digest(raw) != digest {
		return nil, fmt.Errorf("schema digest mismatch")
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, fmt.Errorf("external schema reference denied") }
	const url = "https://model-connection.invalid/schema.json"
	if err := compiler.AddResource(url, bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile(url)
	if err == nil {
		a.compiled[digest] = schema
	}
	return schema, err
}

// parse accepts one JSON object, tolerating surrounding Markdown fences, and
// validates every declared port exactly as the workflow engine will.
func parse(content string, ports []string, schemas map[string]*jsonschema.Schema) (workflow.Values, error) {
	text := strings.TrimSpace(content)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("reply is not a JSON object")
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(text[start : end+1]))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		return nil, fmt.Errorf("reply is not valid JSON: %v", err)
	}
	if len(object) != len(ports) {
		return nil, fmt.Errorf("reply must have exactly the keys %s", strings.Join(ports, ", "))
	}
	result := workflow.Values{}
	for _, name := range ports {
		raw, ok := object[name]
		if !ok {
			return nil, fmt.Errorf("missing key %s", name)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if err := schemas[name].Validate(value); err != nil {
			return nil, fmt.Errorf("port %s: %v", name, err)
		}
		result[name] = raw
	}
	return result, nil
}

// The adapter is also the browser's connection service: it delegates to the
// store and runs connection tests with its own client.
func (a *Adapter) List(ctx context.Context) ([]modelconn.Connection, error) { return a.Store.List(ctx) }
func (a *Adapter) SealConfigured() bool                                     { return a.Store.SealConfigured() }
func (a *Adapter) Save(ctx context.Context, actor, id string, input modelconn.Input) (modelconn.Connection, error) {
	return a.Store.Save(ctx, actor, id, input)
}
func (a *Adapter) StoredKey(ctx context.Context, id string) (string, error) {
	return a.Store.StoredKey(ctx, id)
}
func (a *Adapter) Test(ctx context.Context, settings modelconn.Settings, key string) modelconn.TestResult {
	return a.client.Test(ctx, settings, key)
}
