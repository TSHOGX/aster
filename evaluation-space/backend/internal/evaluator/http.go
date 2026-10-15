package evaluator

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
)

var ErrProviderUnavailable = errors.New("evaluation_provider_unavailable")
var ErrProviderDenied = errors.New("evaluation_provider_denied")

// Connection is operator configuration, never workflow/run input. Credentials
// are effective transport secrets and must not be persisted in job definitions.
type Connection struct {
	Endpoint string
	ClientID string
	Secret   string
}
type HTTPProvider struct {
	service    evolution.Revision
	connection Connection
	client     *http.Client
}

func NewHTTPProvider(service evolution.Revision, connection Connection) (*HTTPProvider, error) {
	if service.Validate() != nil || !manifest.ValidID(connection.ClientID) || len(connection.Secret) < 32 || len(connection.Secret) > 1024 {
		return nil, fmt.Errorf("registered provider identity and credentials required")
	}
	origin, err := url.Parse(connection.Endpoint)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, fmt.Errorf("canonical provider origin required")
	}
	if origin.Scheme != "https" {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote evaluator requires HTTPS")
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: time.Minute, MaxIdleConnsPerHost: 8}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &HTTPProvider{service: service, connection: connection, client: client}, nil
}
func (p *HTTPProvider) Close() { p.client.CloseIdleConnections() }

func (p *HTTPProvider) call(ctx context.Context, method, path string, body any, out any, allowAbsent bool) error {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > evolution.MaxDocumentBytes {
			return fmt.Errorf("invalid provider request")
		}
		input = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.connection.Endpoint+path, input)
	if err != nil {
		return ErrProviderUnavailable
	}
	request.SetBasicAuth(p.connection.ClientID, p.connection.Secret)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Evaluation-Service-ID", p.service.ID)
	request.Header.Set("X-Evaluation-Service-Revision", fmt.Sprint(p.service.Revision))
	request.Header.Set("X-Evaluation-Service-Digest", p.service.Digest)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return ErrProviderDenied
	}
	if response.StatusCode == 404 && allowAbsent {
		value, err := readProviderJSON[struct {
			Error string `json:"error"`
		}](response.Body, 1024)
		if err == nil && value.Error == "evaluation_job_not_found" {
			return ErrProviderJobNotFound
		}
		return ErrProviderUnavailable
	}
	if out == nil {
		if response.StatusCode != 204 {
			return ErrProviderUnavailable
		}
		return nil
	}
	if response.StatusCode != 200 && response.StatusCode != 201 && response.StatusCode != 202 {
		return ErrProviderUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return ErrProviderUnavailable
	}
	// Strict decode rejects duplicate fields and trailing data before copying into
	// the typed destination. Provider diagnostics never escape this boundary.
	switch target := out.(type) {
	case *evolution.JobStatus:
		value, err := evolution.Decode[evolution.JobStatus](bytes.NewReader(raw))
		if err != nil {
			return ErrProviderUnavailable
		}
		*target = value
	case *projections.EvaluationCompletion:
		value, err := evolution.Decode[projections.EvaluationCompletion](bytes.NewReader(raw))
		if err != nil {
			return ErrProviderUnavailable
		}
		*target = value
	default:
		return ErrProviderUnavailable
	}
	return nil
}
func readProviderJSON[T any](reader io.Reader, limit int64) (T, error) {
	var empty T
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return empty, ErrProviderUnavailable
	}
	return evolution.Decode[T](bytes.NewReader(raw))
}
func providerJobPath(id string) (string, error) {
	if !manifest.ValidID(id) {
		return "", fmt.Errorf("invalid provider job ID")
	}
	return "/evaluation/v1/jobs/" + url.PathEscape(id), nil
}
func (p *HTTPProvider) Status(ctx context.Context, id string) (evolution.JobStatus, error) {
	var value evolution.JobStatus
	path, err := providerJobPath(id)
	if err != nil {
		return value, err
	}
	err = p.call(ctx, http.MethodGet, path, nil, &value, true)
	if err == nil && (value.JobID != id || value.Fence < 1) {
		return value, ErrProviderUnavailable
	}
	return value, err
}
func (p *HTTPProvider) Submit(ctx context.Context, submission evolution.EvaluationSubmission) (evolution.JobStatus, error) {
	var value evolution.JobStatus
	path, err := providerJobPath(submission.JobID)
	if err != nil {
		return value, err
	}
	if !manifest.ValidID(submission.RequestID) || !manifest.ValidID(submission.GrantID) || submission.Fence < 1 || submission.Subject.Validate() != nil || submission.Budget.Validate() != nil {
		return value, fmt.Errorf("invalid evaluator submission")
	}
	err = p.call(ctx, http.MethodPut, path, submission, &value, false)
	if err == nil && (value.JobID != submission.JobID || value.Fence != submission.Fence) {
		return value, ErrProviderUnavailable
	}
	return value, err
}
func (p *HTTPProvider) Cancel(ctx context.Context, id string, fence int64) error {
	path, err := providerJobPath(id)
	if err != nil {
		return err
	}
	if fence < 1 {
		return fmt.Errorf("invalid cancellation fence")
	}
	return p.call(ctx, http.MethodPost, path+"/cancel", struct {
		Fence int64 `json:"fence"`
	}{fence}, nil, false)
}
func (p *HTTPProvider) Result(ctx context.Context, id string) (projections.EvaluationCompletion, error) {
	var value projections.EvaluationCompletion
	path, err := providerJobPath(id)
	if err != nil {
		return value, err
	}
	err = p.call(ctx, http.MethodGet, path+"/result", nil, &value, false)
	if err == nil && (value.Result.JobID != id || value.Result.Service != p.service || value.Result.Fence < 1 || value.Usage.Tokens < 0 || value.Usage.CostMicros < 0) {
		return value, ErrProviderUnavailable
	}
	return value, err
}

type ServiceCatalog interface {
	CatalogDefinition(context.Context, string, evolution.Revision) ([]byte, error)
}

// HTTPRegistry resolves only immutable catalog service pins and configured
// connection IDs. The service's current enabled state is checked by authority.
type HTTPRegistry struct {
	Catalog     ServiceCatalog
	Connections map[string]Connection
}

func (r HTTPRegistry) Resolve(ctx context.Context, ref evolution.Revision) (Provider, error) {
	if r.Catalog == nil {
		return nil, ErrProviderUnavailable
	}
	value, err := r.Catalog.CatalogDefinition(ctx, "service", ref)
	if err != nil {
		return nil, err
	}
	service, decodeErr := evolution.Decode[evolution.Service](bytes.NewReader(value))
	if decodeErr != nil || service.Validate() != nil || service.Kind != "evaluation" || service.Ref != ref {
		return nil, ErrProviderUnavailable
	}
	digest, digestErr := evolution.DefinitionDigest(service)
	if digestErr != nil || digest != ref.Digest {
		return nil, ErrProviderUnavailable
	}
	connection, ok := r.Connections[service.ConnectionID]
	if !ok || strings.TrimSpace(connection.Endpoint) == "" {
		return nil, ErrProviderUnavailable
	}
	return NewHTTPProvider(ref, connection)
}

var _ ServiceCatalog = (*projections.Store)(nil)
