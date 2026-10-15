package identity

import (
	"bytes"
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

// AuthorizeCatalog authenticates the inbound central caller before consulting
// central identity with this service's separate registered credential.
func (a *ProjectionAuthority) AuthorizeCatalog(inbound *http.Request, scope httpapi.CatalogScope) error {
	user, password, ok := inbound.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	expected := sha256.Sum256([]byte(a.config.CentralSecret))
	if !ok || user != "central" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		return httpapi.ErrCatalogDenied
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(inbound.Context(), "POST", a.config.Endpoint+"/api/identity/v1/catalog-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	response, err := a.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return httpapi.ErrCatalogDenied
	}
	if response.StatusCode != 200 {
		return ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil || len(raw) > 1024 {
		return ErrUnavailable
	}
	decision, err := evolution.Decode[struct {
		Allowed bool `json:"allowed"`
	}](bytes.NewReader(raw))
	if err != nil {
		return ErrUnavailable
	}
	if !decision.Allowed {
		return httpapi.ErrCatalogDenied
	}
	return nil
}
