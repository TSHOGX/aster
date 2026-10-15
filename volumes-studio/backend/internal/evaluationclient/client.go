package evaluationclient

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
	"time"

	"aster/contracts/evolution"
)

var ErrUnavailable = errors.New("evaluation_unavailable")
var ErrPending = evolution.ErrEvaluationPending
var ErrDenied = errors.New("evaluation_denied")

type Authority interface {
	AuthorizeEvaluation(context.Context, evolution.EvaluationAssignment) (evolution.EvaluationGrant, error)
}
type Client struct {
	endpoint, secret string
	authority        Authority
	http             *http.Client
}

func New(endpoint, secret string, authority Authority) (*Client, error) {
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, fmt.Errorf("canonical evaluation origin required")
	}
	if origin.Scheme != "https" {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote evaluation requires HTTPS")
		}
	}
	if len(secret) < 32 || len(secret) > 1024 || authority == nil {
		return nil, fmt.Errorf("evaluation credentials and authority required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second, TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: time.Minute}
	return &Client{endpoint: endpoint, secret: secret, authority: authority, http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, action string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/evaluation-space/api/v1/evaluations/"+action, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth("studio", c.secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 403 || response.StatusCode == 401 {
		return nil, ErrDenied
	}
	if response.StatusCode == 404 {
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1025))
		if err != nil || len(raw) > 1024 {
			return nil, ErrUnavailable
		}
		value, err := evolution.Decode[struct {
			Error string `json:"error"`
		}](bytes.NewReader(raw))
		if action == "result" && err == nil && value.Error == "evaluation_not_found" {
			return nil, ErrPending
		}
		return nil, ErrUnavailable
	}
	if response.StatusCode != 200 {
		return nil, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, ErrUnavailable
	}
	return raw, nil
}
func (c *Client) Profile(ctx context.Context, scope evolution.EvaluationProfileScope) (evolution.EvaluationProfile, error) {
	var profile evolution.EvaluationProfile
	raw, err := c.call(ctx, "profile", scope)
	if err != nil {
		return profile, err
	}
	profile, err = evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
	if err != nil || profile.Validate() != nil || profile.Ref != scope.Profile || profile.Dataset.GroupID != scope.GroupID {
		return profile, ErrUnavailable
	}
	digest, err := evolution.DefinitionDigest(profile)
	if err != nil || digest != profile.Ref.Digest {
		return profile, ErrUnavailable
	}
	return profile, nil
}
func (c *Client) Authorize(ctx context.Context, assignment evolution.EvaluationAssignment) (evolution.EvaluationGrant, error) {
	return c.authority.AuthorizeEvaluation(ctx, assignment)
}
func (c *Client) Admit(ctx context.Context, grant evolution.EvaluationGrant) error {
	raw, err := c.call(ctx, "admit", grant)
	if err != nil {
		return err
	}
	receipt, err := evolution.Decode[struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}](bytes.NewReader(raw))
	if err != nil || receipt.ID != grant.JobID {
		return ErrUnavailable
	}
	switch receipt.State {
	case "queued", "running", "cancelling", "completed", "error", "canceled":
		return nil
	default:
		return ErrUnavailable
	}
}
func (c *Client) Result(ctx context.Context, grant evolution.EvaluationGrant) (evolution.EvaluationResult, error) {
	var result evolution.EvaluationResult
	raw, err := c.call(ctx, "result", grant)
	if err != nil {
		return result, err
	}
	completion, err := evolution.Decode[struct {
		Result evolution.EvaluationResult `json:"result"`
		Usage  struct {
			Tokens     int64 `json:"tokens"`
			CostMicros int64 `json:"cost_micros"`
		} `json:"usage"`
	}](bytes.NewReader(raw))
	if err != nil {
		return result, ErrUnavailable
	}
	result = completion.Result
	digest, err := result.Subject.Digest()
	if err != nil || result.JobID != grant.JobID || result.Service != grant.Service || digest != grant.SubjectDigest {
		return evolution.EvaluationResult{}, ErrUnavailable
	}
	return result, nil
}

func (c *Client) Verify(ctx context.Context, grant evolution.EvaluationGrant) error {
	raw, err := c.call(ctx, "verify", grant)
	if err != nil {
		return err
	}
	decision, err := evolution.Decode[struct {
		Allowed bool `json:"allowed"`
	}](bytes.NewReader(raw))
	if err != nil || !decision.Allowed {
		return ErrDenied
	}
	return nil
}

func (c *Client) Cancel(ctx context.Context, grant evolution.EvaluationGrant) error {
	_, err := c.call(ctx, "cancel", grant)
	return err
}
