package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/httpapi"
)

var ErrUnavailable = fmt.Errorf("identity_unavailable")
var ErrUnauthorized = fmt.Errorf("identity_unauthorized")

type ProjectionConfig struct{ Endpoint, ClientID, ClientSecret, CentralSecret string }
type ProjectionAuthority struct {
	config ProjectionConfig
	client *http.Client
}

func NewProjectionAuthority(config ProjectionConfig) (*ProjectionAuthority, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("identity endpoint must be an origin")
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote identity requires HTTPS")
		}
	}
	if config.ClientID != "evaluation-space" || len(config.ClientSecret) < 32 || len(config.CentralSecret) < 32 {
		return nil, fmt.Errorf("registered evaluation and central credentials required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	return &ProjectionAuthority{config: config, client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *ProjectionAuthority) AuthorizeProjection(inbound *http.Request, scope httpapi.ProjectionScope) error {
	user, password, ok := inbound.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	expected := sha256.Sum256([]byte(a.config.CentralSecret))
	if !ok || user != "central" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		return httpapi.ErrAuthorityDenied
	}
	body := struct {
		GroupID     string `json:"group_id"`
		OperationID string `json:"operation_id"`
		Revision    int64  `json:"revision"`
		Digest      string `json:"digest"`
	}{scope.GroupID, scope.OperationID, scope.Revision, scope.Digest}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(inbound.Context(), "POST", a.config.Endpoint+"/api/identity/v1/projection-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("central authority unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("central authority unavailable")
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return fmt.Errorf("invalid authority response")
	}
	decision, err := evolution.Decode[struct {
		Allowed bool `json:"allowed"`
	}](bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid authority response")
	}
	if !decision.Allowed {
		return httpapi.ErrAuthorityDenied
	}
	return nil
}

// AuthorizeCatalog asks central identity for current service catalog discovery
// authority; catalog payloads remain separately scoped by the HTTP API.
func (a *ProjectionAuthority) AuthorizeCatalog(ctx context.Context, kind string) error {
	raw, err := json.Marshal(struct {
		Kind string `json:"kind"`
	}{kind})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/catalog-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	resp, err := a.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrUnauthorized
	}
	if resp.StatusCode != 200 {
		return ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil || len(body) > 1024 {
		return ErrUnavailable
	}
	var result struct {
		Allowed bool `json:"allowed"`
	}
	if json.Unmarshal(body, &result) != nil || !result.Allowed {
		return ErrUnauthorized
	}
	return nil
}
